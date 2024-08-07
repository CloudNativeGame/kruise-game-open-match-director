package main

import (
	"context"
	"flag"
	"github.com/CloudNativeGame/kruise-game-open-match-director/pkg"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"
)

func buildConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}
		return cfg, nil
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func main() {
	klog.InitFlags(nil)

	opt := &pkg.Options{}

	var kubeconfig string
	var leaseLockName string
	var leaseLockNamespace string
	var id string

	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&id, "id", uuid.New().String(), "the holder identity name")
	flag.StringVar(&leaseLockName, "lease-lock-name", "kruise-game-open-match-director", "the lease lock resource name")
	flag.StringVar(&leaseLockNamespace, "lease-lock-namespace", "", "the lease lock resource namespace")
	flag.StringVar(&opt.BackendEndpoint, "backend-endpoint", "open-match-backend.open-match.svc.cluster.local", "default open-match backend endpoint.")
	flag.IntVar(&opt.BackendPort, "backend-port", 50505, "default open-match backend port.")
	flag.StringVar(&opt.FrontendEndpoint, "frontend-endpoint", "open-match-frontend.open-match.svc.cluster.local", "default open-match backend endpoint.")
	flag.IntVar(&opt.FrontendPort, "frontend-port", 50504, "default open-match frontend port.")
	flag.StringVar(&opt.MatchFunctionEndpoint, "match-function-endpoint", "open-match-matchfunction.open-match.svc.cluster.local", "default open-match matchfunction endpoint.")
	flag.IntVar(&opt.MatchFunctionPort, "match-function-port", 50502, "default open-match front port.")
	flag.StringVar(&opt.GameServerLabelSelector, "game-server-label-selector", "open-match-demo", "default game server labelSelector.")
	flag.DurationVar(&opt.GameServersReSyncInterval, "game-server-resync-interval", time.Second*30, "re-sync game servers interval,default: 30x")
	flag.DurationVar(&opt.MatchPullingInterval, "match-pulling-interval", time.Second*5, "match pulling interval,default: 5s")
	flag.StringVar(&opt.GameServerSetNames, "gss-names", "", "the names of GameServerSets perceived by director")
	flag.StringVar(&opt.ProfileName, "profile-name", "default", "")
	flag.StringVar(&opt.SlaveClustersNames, "slave-clusters", "", "Specify the slave cluster names, which are also the names of corresponding kubeconfig secrets. Format is {cluster_1_secret_name},{cluster_2_secret_name},...")
	flag.Parse()

	if leaseLockName == "" {
		klog.Fatal("unable to get lease lock resource name (missing lease-lock-name flag).")
	}

	if leaseLockNamespace == "" {
		klog.Fatal("unable to get lease lock resource namespace (missing lease-lock-namespace flag).")
	}

	opt.Namespace = leaseLockNamespace

	// leader election uses the Kubernetes API by writing to a
	// lock object, which can be a LeaseLock object (preferred),
	// a ConfigMap, or an Endpoints (deprecated) object.
	// Conflicting writes are detected and each client handles those actions
	// independently.
	config, err := buildConfig(kubeconfig)
	if err != nil {
		klog.Fatalf("Failed to build kubeConfig,because of %v", err)
	}

	loopStopCh := make(chan struct{})
	loopDoneCh := make(chan struct{})

	// run loop
	run := func(ctx context.Context) {
		// complete your controller loop here
		klog.Info("Controller loop started")

		opt.Config = config
		allocator, err := pkg.NewAllocator(opt)

		if err != nil {
			log.Fatalf("Failed to initialize Allocator,because of %v", err)
		}

		// Run main loop
		allocator.Run(loopStopCh, loopDoneCh)
	}

	// use a Go context so we can tell the leaderelection code when we
	// want to step down
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		close(loopStopCh)
		<-loopDoneCh
		cancel()
	}()

	// listen for interrupts or the Linux SIGTERM signal and cancel
	// our context, which the leader election code will observe and
	// step down
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		klog.Info("Received termination, signaling shutdown")
		close(loopStopCh)
		<-loopDoneCh
		cancel()
	}()

	// create clint-go clientSet
	client := clientset.NewForConfigOrDie(config)
	// we use the Lease lock type since edits to Leases are less common
	// and fewer objects in the cluster watch "all Leases".
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      leaseLockName,
			Namespace: leaseLockNamespace,
		},
		Client: client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: id,
		},
	}

	// start the leader election code loop
	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock: lock,
		// IMPORTANT: you MUST ensure that any code you have that
		// is protected by the lease must terminate **before**
		// you call cancel. Otherwise, you could have a background
		// loop still running and another process could
		// get elected before your background loop finished, violating
		// the stated goal of the lease.
		ReleaseOnCancel: true,
		LeaseDuration:   60 * time.Second,
		RenewDeadline:   15 * time.Second,
		RetryPeriod:     5 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				// we're notified when we start - this is where you would
				// usually put your code
				run(ctx)
			},
			OnStoppedLeading: func() {
				// we can do cleanup here
				klog.Infof("leader lost: %s", id)
				os.Exit(0)
			},
			OnNewLeader: func(identity string) {
				// we're notified when new leader elected
				if identity == id {
					// I just got the lock
					return
				}
				klog.Infof("new leader elected: %s", identity)
			},
		},
	})
}
