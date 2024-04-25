package pkg

import (
	"context"
	"fmt"
	"github.com/golang/protobuf/ptypes/wrappers"
	"github.com/openkruise/kruise-game/apis/v1alpha1"
	"github.com/openkruise/kruise-game/pkg/client/clientset/versioned"
	v1alpha1client "github.com/openkruise/kruise-game/pkg/client/clientset/versioned"
	"github.com/openkruise/kruise-game/pkg/client/informers/externalversions"
	v1alpha1Lister "github.com/openkruise/kruise-game/pkg/client/listers/apis/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
	"io"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	log "k8s.io/klog/v2"
	"open-match.dev/open-match/pkg/pb"
	"strings"
	"sync"
	"time"
)

const (
	BackfillConnectionKey     = "game.kruise.io/connection"
	BackfillConnectedTag      = "game.kruise.io/connected"
	OpenMatchLabelSelectorKey = "game.kruise.io/owner-gss"
  AssignmentGsNameKey       = "game.kruise.io/gs-name"
	GameServerMatchIdKey      = "gs-sync/match-id"
	GameNameProfileKey        = "game_name"
)

type ConnectInfo struct {
	address string
	gsName  string
}

type Allocator struct {
	gameServerInformerFactory externalversions.SharedInformerFactory
	GameServerLister          v1alpha1Lister.GameServerLister
	GameServerClient          v1alpha1client.Interface

	BackendClient  pb.BackendServiceClient
	BackendConn    *grpc.ClientConn
	FrontendClient pb.FrontendServiceClient
	FrontendConn   *grpc.ClientConn

	MatchFunctionEndpoint string
	MatchFunctionPort     int32

	GameServerLabelSelector string
	ProfileName             string
	GameServerSetNames      string

	//GameServersReSyncInterval time.Duration
	MatchPullingInterval time.Duration
}

func NewAllocator(options *Options) (allocator *Allocator, err error) {

	backendConnStr, err := options.GetBackendConn()
	if err != nil {
		log.Errorf("Failed to init backend conn,because of %v", err)
		return
	}

	backendConn, err := grpc.Dial(backendConnStr, grpc.WithInsecure())
	if err != nil {
		log.Errorf("Failed to connect to Open Match Backend, got %v", err)
		return
	}

	be := pb.NewBackendServiceClient(backendConn)

	frontendConnStr, err := options.GetFrontendConn()
	if err != nil {
		log.Errorf("Failed to init frontend conn,because of %v", err)
		return
	}

	frontendConn, err := grpc.Dial(frontendConnStr, grpc.WithInsecure())
	if err != nil {
		log.Errorf("Failed to connect to Open Match Frontend, got %v", err)
		return
	}

	fe := pb.NewFrontendServiceClient(frontendConn)

	config := options.Config
	kruiseGameClient, err := versioned.NewForConfig(config)

	if err != nil {
		log.Errorf("Failed to create kruise game client,because of %v", err)
		return
	}

	gameServerInformerFactory := externalversions.NewSharedInformerFactory(kruiseGameClient, options.GameServersReSyncInterval)

	// Create GameServer informer by informerFactory
	gameServers := gameServerInformerFactory.Game().V1alpha1().GameServers()

	return &Allocator{
		BackendConn:               backendConn,
		FrontendConn:              frontendConn,
		gameServerInformerFactory: gameServerInformerFactory,
		BackendClient:             be,
		FrontendClient:            fe,
		MatchFunctionEndpoint:     options.MatchFunctionEndpoint,
		MatchFunctionPort:         int32(options.MatchFunctionPort),
		GameServerLister:          gameServers.Lister(),
		GameServerClient:          kruiseGameClient,
		GameServerLabelSelector:   options.GameServerLabelSelector,
		ProfileName:               options.ProfileName,
		GameServerSetNames:        options.GameServerSetNames,
		MatchPullingInterval:      options.MatchPullingInterval,
	}, nil
}

func (a *Allocator) Run() {
	log.Info("Ready to run allocator service")
	// Generate the profiles to fetch matches for.
	defer a.BackendConn.Close()

	profiles := a.generateProfiles()

	stopCh := make(chan struct{})
	defer close(stopCh)

	go a.gameServerInformerFactory.Start(stopCh)

	informerHasSynced := a.gameServerInformerFactory.WaitForCacheSync(stopCh)

	for informer, synced := range informerHasSynced {
		if synced == false {
			log.Fatalf("Failed to sync informer %v", informer)
		}
	}

	log.Info("All informer has synced")

	for range time.Tick(a.MatchPullingInterval) {
		log.Infof("Fetch Matches from store in every %v seconds", a.MatchPullingInterval)
		// Fetch matches for each profile and make random assignments for Tickets in
		// the matches returned.
		var wg sync.WaitGroup
		for _, p := range profiles {
			wg.Add(1)
			go func(wg *sync.WaitGroup, p *pb.MatchProfile) {
				defer wg.Done()
				matches, err := a.fetch(p)
				if err != nil {
					log.Warningf("Failed to fetch matches for profile %v, got %s", p.GetName(), err.Error())
					return
				}

				log.Infof("Generated %v matches for profile %v", len(matches), p.GetName())
				for _, match := range matches {
					err = a.assignMatch(match)
					if err != nil {
						log.Errorf("Failed to assignMatch to %s,because of %s", match.MatchId, err.Error())
					}
				}
			}(&wg, p)
		}

		wg.Wait()
	}
}

func (a *Allocator) fetch(p *pb.MatchProfile) ([]*pb.Match, error) {
	req := &pb.FetchMatchesRequest{
		Config: &pb.FunctionConfig{
			Host: a.MatchFunctionEndpoint,
			Port: a.MatchFunctionPort,
			Type: pb.FunctionConfig_GRPC,
		},
		Profile: p,
	}

	stream, err := a.BackendClient.FetchMatches(context.Background(), req)
	if err != nil {
		return nil, fmt.Errorf("error on fetch match request from backend: %w", err)
	}

	var result []*pb.Match
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("error on fetch match stream recieving: %w", err)
		}

		result = append(result, resp.GetMatch())
	}

	return result, nil
}

// assignMatch assigns `match`. If we fail, abandon the tickets - we'll catch it next loop.
func (a *Allocator) assignMatch(match *pb.Match) error {
	backfill, connectInfo, err := a.getBackfillConn(match)
	if err != nil {
		return err
	}
	log.Infof("The backfill in the match %v is %v", match, backfill)
	if connectInfo == nil {
		chosenGameServer, err := a.choseGameServer(match)
		if err != nil {
			return err
		}

		// get external addresses
		externalAddress := chosenGameServer.Status.NetworkStatus.ExternalAddresses[0]
		addr := fmt.Sprintf("%s:%s", externalAddress.IP, externalAddress.Ports[0].Port)

		// get gsName
		gsName := chosenGameServer.GetNamespace() + "/" + chosenGameServer.GetName()

		patchData := []byte(fmt.Sprintf(`
		{
            "metadata": {
				"annotations": 
				{
					%s: %s
				}
            },
			"spec": {
				"opsState": "Allocated"
			}
		}`, GameServerMatchIdKey, match.GetMatchId()))

		_, err = a.GameServerClient.GameV1alpha1().GameServers(chosenGameServer.Namespace).Patch(context.Background(), chosenGameServer.Name, types.MergePatchType, patchData, metav1.PatchOptions{})

		if err != nil {
			log.Errorf("Failed to make match because of %s", err.Error())
			return err
		}

		log.Infof("GameServer %s has been allocated.", chosenGameServer.Name)

		if backfill != nil {
			err = a.updateBackfill(backfill, &ConnectInfo{
				address: addr,
				gsName:  gsName,
			})
			if err != nil {
				return err
			}
		}
	}

	if err := a.assignConnToTickets(connectInfo, match); err != nil {
		log.Errorf("Could not assign connection %v to match %s: %v", connectInfo, match.GetMatchId(), err)
		return err
	}

	log.Infof("Assigned %v to match %s", connectInfo, match.GetMatchId())
	return nil
}

func (a *Allocator) getBackfillConn(match *pb.Match) (*pb.Backfill, *ConnectInfo, error) {
	backfill := match.Backfill
	if backfill != nil {
		if backfill.Extensions != nil {
			var addr string
			var gsName string
			if anyConn, ok := backfill.Extensions[BackfillConnectionKey]; ok {
				var val wrappers.StringValue
				err := anyConn.UnmarshalTo(&val)
				if err != nil {
					log.Errorf("Unmarshal Backfill %s Extensions-BackfillConnectionKey failed", backfill.Id)
					return nil, nil, err
				}
				addr = val.Value
			}
			if anyGsName, ok := backfill.Extensions[AssignmentGsNameKey]; ok {
				var val wrappers.StringValue
				err := anyGsName.UnmarshalTo(&val)
				if err != nil {
					log.Errorf("Unmarshal Backfill %s Extensions-AssignmentGsNameKey failed", backfill.Id)
					return nil, nil, err
				}
				gsName = val.Value
			}
			return backfill, &ConnectInfo{
				address: addr,
				gsName:  gsName,
			}, nil
		}
		return backfill, nil, nil
	}
	return nil, nil, nil
}

func (a *Allocator) choseGameServer(match *pb.Match) (*v1alpha1.GameServer, error) {
	gssName := match.MatchProfile
	if match.MatchProfile == a.ProfileName {
		gssName = a.GameServerSetNames
	}
	labelSelector := metav1.LabelSelector{
		MatchLabels: map[string]string{
			OpenMatchLabelSelectorKey: gssName,
		},
	}

	selector, err := metav1.LabelSelectorAsSelector(&labelSelector)
	if err != nil {
		log.Errorf("Failed to create correct label selector,because of %s", err.Error())
		return nil, err
	}

	gameServers, err := a.GameServerLister.List(selector)
	if err != nil {
		log.Errorf("Failed to list game servers,because of %s", err.Error())
		return nil, err
	}

	gameServerPools := make([]*v1alpha1.GameServer, 0)

	for _, gs := range gameServers {
		if gs.Status.CurrentState == v1alpha1.Ready && gs.Spec.OpsState == v1alpha1.None && gs.Status.NetworkStatus.CurrentNetworkState == v1alpha1.NetworkReady {
			gameServerPools = append(gameServerPools, gs)
		}
	}

	chosenGameServer := &v1alpha1.GameServer{}

	log.Infof("%d game servers ready to be matched.", len(gameServerPools))

	if len(gameServerPools) > 0 {
		chosenGameServer = gameServerPools[0]
	} else {
		return nil, fmt.Errorf("No enough game servers(%d) to be matched,maybe you need to scale out more game servers.", len(gameServers))
	}

	return chosenGameServer, nil
}

func (a *Allocator) updateBackfill(backfill *pb.Backfill, conn *ConnectInfo) error {
	// update backfill connection info
	if backfill.Extensions == nil {
		backfill.Extensions = make(map[string]*anypb.Any)
	}

	anyAddr, err := anypb.New(&wrappers.StringValue{Value: conn.address})
	if err != nil {
		return err
	}
	backfill.Extensions[BackfillConnectionKey] = anyAddr

	anyGsName, err := anypb.New(&wrappers.StringValue{Value: conn.gsName})
	if err != nil {
		return err
	}
	backfill.Extensions[AssignmentGsNameKey] = anyGsName

	backfill.SearchFields.Tags = append(backfill.SearchFields.Tags, BackfillConnectedTag)

	req := &pb.UpdateBackfillRequest{
		Backfill: backfill,
	}
	_, err = a.FrontendClient.UpdateBackfill(context.Background(), req)
	if err != nil {
		return err
	}
	return nil
}

func (a *Allocator) assignConnToTickets(conn *ConnectInfo, match *pb.Match) error {
	ticketIDs := []string{}
	for _, t := range match.GetTickets() {
		ticketIDs = append(ticketIDs, t.Id)
	}

	assignmentExtensions := match.Extensions
	if assignmentExtensions == nil {
		assignmentExtensions = make(map[string]*anypb.Any)
	}
	anyGsName, err := anypb.New(&wrappers.StringValue{Value: conn.gsName})
	if err != nil {
		return err
	}
	assignmentExtensions[AssignmentGsNameKey] = anyGsName

	req := &pb.AssignTicketsRequest{
		Assignments: []*pb.AssignmentGroup{
			{
				TicketIds: ticketIDs,
				Assignment: &pb.Assignment{
					Connection: conn.address,
					Extensions: assignmentExtensions,
				},
			},
		},
	}

	_, err = a.BackendClient.AssignTickets(context.Background(), req)
	return err
}

func (a *Allocator) generateProfiles() []*pb.MatchProfile {
	var profiles []*pb.MatchProfile
	for _, gssName := range strings.Split(a.GameServerSetNames, ",") {
		profiles = append(profiles, &pb.MatchProfile{
			Name: gssName,
			Pools: []*pb.Pool{{
				Name: GameNameProfileKey,
				StringEqualsFilters: []*pb.StringEqualsFilter{
					{
						StringArg: GameNameProfileKey,
						Value:     gssName,
					},
				},
			}},
		})
	}
	profiles = append(profiles, &pb.MatchProfile{
		Name: a.ProfileName,
		Pools: []*pb.Pool{{
			Name: a.ProfileName,
		}},
	})
	return profiles
}
