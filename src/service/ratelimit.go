package ratelimit

import (
	"strings"
	"sync"
	"time"

	pb "github.com/envoyproxy/go-control-plane/envoy/service/ratelimit/v3"
	"github.com/lyft/goruntime/loader"
	stats "github.com/lyft/gostats"
	"github.com/replicon/ratelimit/src/assert"
	"github.com/replicon/ratelimit/src/config"
	"github.com/replicon/ratelimit/src/limiter"
	"github.com/replicon/ratelimit/src/metrics"
	"github.com/replicon/ratelimit/src/redis"
	"github.com/replicon/ratelimit/src/util"
	logger "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

type shouldRateLimitStats struct {
	redisError         stats.Counter
	serviceError       stats.Counter
	wouldOfRateLimited stats.Counter
}

func newShouldRateLimitStats(scope stats.Scope) shouldRateLimitStats {
	ret := shouldRateLimitStats{}
	ret.redisError = scope.NewCounter("redis_error")
	ret.serviceError = scope.NewCounter("service_error")
	ret.wouldOfRateLimited = scope.NewCounter("shadow_block")
	return ret
}

type serviceStats struct {
	configLoadSuccess stats.Counter
	configLoadError   stats.Counter
	shouldRateLimit   shouldRateLimitStats
}

func newServiceStats(scope stats.Scope) serviceStats {
	ret := serviceStats{}
	ret.configLoadSuccess = scope.NewCounter("config_load_success")
	ret.configLoadError = scope.NewCounter("config_load_error")
	ret.shouldRateLimit = newShouldRateLimitStats(scope.Scope("call.should_rate_limit"))
	return ret
}

type RateLimitServiceServer interface {
	pb.RateLimitServiceServer
	GetCurrentConfig() config.RateLimitConfig
	GetLegacyService() RateLimitLegacyServiceServer
}

type service struct {
	runtime            loader.IFace
	configLock         sync.RWMutex
	configLoader       config.RateLimitConfigLoader
	config             config.RateLimitConfig
	runtimeUpdateEvent chan int
	cache              limiter.RateLimitCache
	stats              serviceStats
	rlStatsScope       stats.Scope
	legacy             *legacyService
	shadowMode         bool
	runtimeWatchRoot   bool
}

func (this *service) reloadConfig() {
	defer func() {
		if e := recover(); e != nil {
			configError, ok := e.(config.RateLimitConfigError)
			if !ok {
				panic(e)
			}

			this.stats.configLoadError.Inc()
			metrics.RateLimitErrors.WithLabelValues("config_reload").Inc()
			logger.Errorf("error loading new configuration from runtime: %s", configError.Error())
		}
	}()

	files := []config.RateLimitConfigToLoad{}
	snapshot := this.runtime.Snapshot()
	for _, key := range snapshot.Keys() {
		if this.runtimeWatchRoot && !strings.HasPrefix(key, "config.") {
			continue
		}

		files = append(files, config.RateLimitConfigToLoad{key, snapshot.Get(key)})
	}

	newConfig := this.configLoader.Load(files, this.rlStatsScope)
	this.stats.configLoadSuccess.Inc()
	this.configLock.Lock()
	this.config = newConfig
	this.configLock.Unlock()

}

type serviceError string

func (e serviceError) Error() string {
	return string(e)
}

func checkServiceErr(something bool, msg string) {
	if !something {
		panic(serviceError(msg))
	}
}

func (this *service) shouldRateLimitWorker(
	ctx context.Context, request *pb.RateLimitRequest) *pb.RateLimitResponse {

	checkServiceErr(request.Domain != "", "rate limit domain must not be empty")
	checkServiceErr(len(request.Descriptors) != 0, "rate limit descriptor list must not be empty")

	snappedConfig := this.GetCurrentConfig()
	checkServiceErr(snappedConfig != nil, "no rate limit configuration loaded")

	limitsToCheck := make([]*config.RateLimit, len(request.Descriptors))
	for i, descriptor := range request.Descriptors {
		limitsToCheck[i] = snappedConfig.GetLimit(ctx, request.Domain, descriptor)
	}

	responseDescriptorStatuses := this.cache.DoLimit(ctx, request, limitsToCheck)
	assert.Assert(len(limitsToCheck) == len(responseDescriptorStatuses))

	response := &pb.RateLimitResponse{}
	response.Statuses = make([]*pb.RateLimitResponse_DescriptorStatus, len(request.Descriptors))
	finalCode := pb.RateLimitResponse_OK
	for i, descriptorStatus := range responseDescriptorStatuses {
		response.Statuses[i] = descriptorStatus
		if descriptorStatus.Code == pb.RateLimitResponse_OVER_LIMIT {
			finalCode = descriptorStatus.Code
		}
	}

	response.OverallCode = finalCode
	return response
}

func (this *service) ShouldRateLimit(
	ctx context.Context,
	request *pb.RateLimitRequest) (finalResponse *pb.RateLimitResponse, finalError error) {
	start := time.Now()

	defer func(t time.Time) {
		metrics.RateLimitRequestSummary.Observe(time.Now().Sub(start).Seconds())
	}(start)

	defer func() {
		err := recover()
		if err == nil {
			return
		}

		logger.Debugf("caught error during call")
		finalResponse = nil
		switch t := err.(type) {
		case redis.RedisError:
			{
				this.stats.shouldRateLimit.redisError.Inc()
				metrics.RateLimitErrors.WithLabelValues("redis").Inc()
				finalError = t
			}
		case serviceError:
			{
				this.stats.shouldRateLimit.serviceError.Inc()
				metrics.RateLimitErrors.WithLabelValues("service").Inc()
				finalError = t
			}
		default:
			panic(err)
		}
	}()

	response := this.shouldRateLimitWorker(ctx, request)
	if response.OverallCode != pb.RateLimitResponse_OK {
		for i, descriptorStatus := range response.Statuses {
			if descriptorStatus.Code == pb.RateLimitResponse_OVER_LIMIT {
				descriptor := request.Descriptors[i]
				metricsDescriptor := util.ConvertToMetricsDescriptor(descriptorStatus, descriptor)
				labels := map[string]string{"descriptor_key": metricsDescriptor.Key, "descriptor_value": metricsDescriptor.Value, "limit": metricsDescriptor.Limit, "unit": metricsDescriptor.Unit}
				if this.shadowMode {
					metrics.ShadowRequests.With(labels).Inc()
				} else {
					metrics.LimitedRequests.With(labels).Inc()
				}
			}
		}
	}
	if this.shadowMode {
		response.OverallCode = pb.RateLimitResponse_OK
	}
	logger.Debugf("returning normal response")
	return response, nil
}

func (this *service) GetLegacyService() RateLimitLegacyServiceServer {
	return this.legacy
}

func (this *service) GetCurrentConfig() config.RateLimitConfig {
	this.configLock.RLock()
	defer this.configLock.RUnlock()
	return this.config
}

func NewService(runtime loader.IFace, cache limiter.RateLimitCache,
	configLoader config.RateLimitConfigLoader, stats stats.Scope, shadowMode bool, runtimeWatchRoot bool) RateLimitServiceServer {

	newService := &service{
		runtime:            runtime,
		configLock:         sync.RWMutex{},
		configLoader:       configLoader,
		config:             nil,
		runtimeUpdateEvent: make(chan int),
		cache:              cache,
		stats:              newServiceStats(stats),
		rlStatsScope:       stats.Scope("rate_limit"),
		shadowMode:         shadowMode,
		runtimeWatchRoot:   runtimeWatchRoot,
	}
	newService.legacy = &legacyService{
		s:                          newService,
		shouldRateLimitLegacyStats: newShouldRateLimitLegacyStats(stats),
	}

	runtime.AddUpdateCallback(newService.runtimeUpdateEvent)

	newService.reloadConfig()
	go func() {
		// No exit right now.
		for {
			logger.Debugf("waiting for runtime update")
			<-newService.runtimeUpdateEvent
			logger.Debugf("got runtime update and reloading config")
			newService.reloadConfig()
		}
	}()

	return newService
}
