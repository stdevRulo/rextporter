package client

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Client to get remote data.
type Client interface {
	// GetData will get tha date based on a URL(but can be a cached value for example).
	GetData(metricsCollector chan<- prometheus.Metric) (body []byte, err error)
}

type baseClient struct {
	jobName                        string
	instanceName                   string
	datasource                     string
	datasourceResponseDurationDesc *prometheus.Desc
}

// CacheableClient should return a key(DataPath) for catching resource values
type CacheableClient interface {
	Client
	DataPath() string
}

// Factory can create different kind of clients
type Factory interface {
	CreateClient() (cl Client, err error)
}

type baseFactory struct {
	jobName                        string
	instanceName                   string
	datasource                     string
	datasourceResponseDurationDesc *prometheus.Desc
}

// CacheableFactory can create different kind of cacheable clients
type CacheableFactory interface {
	CreateClient() (cl CacheableClient, err error)
}

type baseCacheableClient string

// DataPath is the endpoint in the case of http clients
func (cl baseCacheableClient) DataPath() string {
	return string(cl)
}

// TODO(denisacostaq@gmail.com): check out http://localhost:6060/pkg/github.com/prometheus/client_golang/api/#NewClient
