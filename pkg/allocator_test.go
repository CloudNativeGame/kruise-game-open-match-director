package pkg

import (
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"reflect"
	"testing"
	"time"
)

func TestGenerateProfiles(t *testing.T) {
	tests := []struct {
		allocator    *Allocator
		profileNames []string
	}{
		{
			allocator: &Allocator{
				ClusterNames:       []string{ClusterHostName},
				GameServerSetNames: "gss-sample",
			},
			profileNames: []string{"gss-sample"},
		},
		{
			allocator: &Allocator{
				ClusterNames:       []string{ClusterHostName, "region-b"},
				GameServerSetNames: "gss-sample",
			},
			profileNames: []string{"gss-sample_Host", "gss-sample_region-b"},
		},
	}

	for _, test := range tests {
		profiles := test.allocator.generateProfiles()
		var profileNames []string
		for _, profile := range profiles {
			profileNames = append(profileNames, profile.Name)
		}
		if !reflect.DeepEqual(profileNames, test.profileNames) {
			t.Errorf("generate profiles failed, expected %v, got %v", test.profileNames, profileNames)
		}
	}
}

func TestNewAllocator(t *testing.T) {
	config, err := clientcmd.BuildConfigFromFlags("", "")
	if err != nil {
		klog.Fatalf("Failed to build kubeConfig,because of %v", err)
	}

	options := &Options{
		BackendEndpoint:           "open-match-backend.open-match.svc.cluster.local",
		BackendPort:               50505,
		FrontendEndpoint:          "open-match-frontend.open-match.svc.cluster.local",
		FrontendPort:              50504,
		MatchFunctionEndpoint:     "open-match-matchfunction.open-match.svc.cluster.local",
		MatchFunctionPort:         50502,
		GameServersReSyncInterval: time.Second * 30,
		MatchPullingInterval:      time.Second * 5,
		GameServerSetNames:        "flappy-bird",
		Config:                    config,
		Namespace:                 "open-match",
		SlaveClustersNames:        "",
	}

	allocator, err := NewAllocator(options)
	if err != nil {
		t.Errorf("Failed to create allocator: %v", err)
		return
	}
	if !reflect.DeepEqual(allocator.ClusterNames, []string{"Host"}) {
		t.Errorf("Failed to create allocator, cluster names should be: %v", []string{"Host"})
	}
}
