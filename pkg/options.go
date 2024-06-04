package pkg

import (
	"fmt"
	"k8s.io/client-go/rest"
	"time"
)

type Options struct {
	MatchFunctionEndpoint     string
	MatchFunctionPort         int
	BackendEndpoint           string
	BackendPort               int
	FrontendEndpoint          string
	FrontendPort              int
	Config                    *rest.Config
	GameServerLabelSelector   string
	GameServersReSyncInterval time.Duration
	MatchPullingInterval      time.Duration
	ProfileName               string
	SlaveClustersNames        string
	Namespace                 string
	GameServerSetNames        string
}

// GetBackendConn return backend conn of open-match service
func (o *Options) GetBackendConn() (string, error) {
	return fmt.Sprintf("%s:%d", o.BackendEndpoint, o.BackendPort), nil
}

// GetFrontendConn return backend conn of open-match service
func (o *Options) GetFrontendConn() (string, error) {
	return fmt.Sprintf("%s:%d", o.FrontendEndpoint, o.FrontendPort), nil
}
