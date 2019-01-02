package exporter

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"

	"github.com/NYTimes/gziphandler"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/simelo/rextporter/src/cache"
	"github.com/simelo/rextporter/src/config"
	"github.com/simelo/rextporter/src/scrapper"
	"github.com/simelo/rextporter/src/util"
	"github.com/simelo/rextporter/src/util/metrics"
	mutil "github.com/simelo/rextporter/src/util/metrics"
	log "github.com/sirupsen/logrus"
)

func autolabelSelftMetrics(metrics []byte, listenAddr string) (labeledMetrics []byte, err error) {
	job := config.KeyLabelJob
	jobValue := config.SystemProgramName
	instance := config.KeyLabelInstance
	instanceValue := listenAddr
	var mtrsWithOutLabels []string
	// NOTE(denisacostaq@gmail.com): all scrapped metrics should have at least the
	// job and instance labels, so the metrics without labels are the autogenerated by
	// golang client library, like: go_gc_duration_seconds, go_memstats_sys_bytes, ...
	if mtrsWithOutLabels, err = mutil.FindMetricsNamesWithoutLabels(metrics, []string{job, instance}); err != nil {
		log.WithError(err).Errorln("ca not find unlabeled metrics")
		return nil, err
	}
	labeledMetrics, err = mutil.AppendLables(
		mtrsWithOutLabels,
		metrics,
		[]*io_prometheus_client.LabelPair{
			&io_prometheus_client.LabelPair{
				Name:  &job,
				Value: &jobValue,
			},
			&io_prometheus_client.LabelPair{
				Name:  &instance,
				Value: &instanceValue,
			},
		},
	)
	if err != nil {
		log.WithError(err).Errorln("Can not append default labels for self metric inside rextporter")
		return nil, config.ErrKeyDecodingFile
	}
	return labeledMetrics, err
}

func mergeMetrics(scrappedMetrics, fordwadedMetrics []byte) (mergedMetrics []byte, err error) {
	recordMetrics := func(metricsRecorder map[string]*io_prometheus_client.MetricFamily, metrics []byte) (err error) {
		var parser expfmt.TextParser
		in := bytes.NewReader(metrics)
		var metricsFamilies map[string]*io_prometheus_client.MetricFamily
		if metricsFamilies, err = parser.TextToMetricFamilies(in); err != nil {
			log.WithError(err).Errorln("error, reading text format failed")
			return config.ErrKeyDecodingFile
		}
		for key, mf := range metricsFamilies {
			if mmf, ok := metricsRecorder[key]; ok {
				mtrs := make([]*io_prometheus_client.Metric, len(mf.Metric)+len(mmf.Metric))
				for idxMetric := range mf.Metric {
					mtrs[idxMetric] = mf.Metric[idxMetric]
				}
				for idxMetric := range mmf.Metric {
					mtrs[len(mf.Metric)+idxMetric] = mmf.Metric[idxMetric]
				}
				mmf.Metric = mtrs
			} else {
				metricsRecorder[key] = mf
			}
		}
		return nil
	}
	var mergedMetricFamilies = make(map[string]*io_prometheus_client.MetricFamily)
	if err = recordMetrics(mergedMetricFamilies, scrappedMetrics); err != nil {
		log.WithError(err).Errorln("can not record scrapped metrics")
		return mergedMetrics, config.ErrKeyDecodingFile
	}
	if err = recordMetrics(mergedMetricFamilies, fordwadedMetrics); err != nil {
		log.WithError(err).Errorln("can not record fordwaded metrics")
		return mergedMetrics, config.ErrKeyDecodingFile
	}
	var buff bytes.Buffer
	writer := bufio.NewWriter(&buff)
	encoder := expfmt.NewEncoder(writer, expfmt.FmtText)
	for _, mf := range mergedMetricFamilies {
		if err := encoder.Encode(mf); err != nil {
			log.WithFields(log.Fields{"err": err, "metric_family": mf}).Errorln("can not encode metric family")
			return mergedMetrics, err
		}
	}
	writer.Flush()
	mergedMetrics = buff.Bytes()
	return mergedMetrics, err
}

func exposedMetricsMiddleware(listenAddr string, scrappers []scrapper.FordwaderScrapper, promHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(listenAddr) == 0 {
			listenAddr = r.Host
		}
		getScrappedMetricFromAPI := func() (data []byte, err error) {
			generalScopeErr := "error reding default data"
			recorder := httptest.NewRecorder()
			promHandler.ServeHTTP(recorder, r)
			var reader io.ReadCloser
			switch recorder.Header().Get("Content-Encoding") {
			case "gzip":
				reader, err = gzip.NewReader(recorder.Body)
				if err != nil {
					errCause := fmt.Sprintln("can not create gzip reader.", err.Error())
					return nil, util.ErrorFromThisScope(errCause, generalScopeErr)
				}
			default:
				reader = ioutil.NopCloser(bytes.NewReader(recorder.Body.Bytes()))
			}
			defer reader.Close()
			if data, err = ioutil.ReadAll(reader); err != nil {
				log.WithError(err).Errorln("can not read recorded default data")
				return nil, err
			}
			labeled, err := autolabelSelftMetrics(data, listenAddr)
			if err != nil {
				log.WithError(err).Errorln("Can not append default labels for self metric inside rextporter")
				return nil, config.ErrKeyDecodingFile
			}
			return labeled, nil
		}
		var err error
		var scrappedFromAPIData []byte
		if scrappedFromAPIData, err = getScrappedMetricFromAPI(); err != nil {
			log.WithError(err).Errorln("error getting default data")
		}
		var allFordwadedData []byte
		for _, fs := range scrappers {
			var iMetrics interface{}
			var err error
			if iMetrics, err = fs.GetMetric(); err != nil {
				log.WithError(err).Errorln("error scrapping fordwader metrics")
			} else {
				fordwadedData, okFordwadedData := iMetrics.([]byte)
				if okFordwadedData {
					allFordwadedData = append(allFordwadedData, fordwadedData...)
				} else {
					log.WithError(err).Errorln("error asserting fordwader metrics data as []byte")
				}
			}
		}
		var allData []byte
		if allData, err = mergeMetrics(scrappedFromAPIData, allFordwadedData); err == nil {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			if count, err := w.Write(allData); err != nil || count != len(allData) {
				if err != nil {
					log.WithError(err).Errorln("error writing data")
				}
				if count != len(allData) {
					log.WithFields(log.Fields{
						"wrote":    count,
						"required": len(allData),
					}).Errorln("no enough content wrote")
				}
			}
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			log.WithError(err).Errorln("merge metrics")
		}
	})
}

// MustExportMetrics will read the config from mainConfigFile if any or use a default one.
func MustExportMetrics(listenAddr, handlerEndpoint string, listenPort uint16, conf config.RextRoot) (srv *http.Server) {
	c := cache.NewCache()
	var collector prometheus.Collector
	var err error
	if collector, err = newMetricsCollector(c, conf); err != nil {
		log.WithError(err).Panicln("Can not create metrics")
	}
	prometheus.MustRegister(collector)
	fDefMetrics := metrics.NewDefaultFordwaderMetrics()
	fDefMetrics.MustRegister()
	var metricsForwaders []scrapper.FordwaderScrapper
	if metricsForwaders, err = createMetricsForwaders(conf, fDefMetrics); err != nil {
		log.WithError(err).Panicln("Can not create forward_metrics metrics")
	}
	var listenAddrPort string
	if len(listenAddr) == 0 {
		listenAddrPort = fmt.Sprintf(":%d", listenPort)
	} else {
		listenAddrPort = fmt.Sprintf("%s:%d", listenAddr, listenPort)
		listenAddr = listenAddrPort
	}
	srv = &http.Server{Addr: listenAddrPort}
	http.Handle(
		handlerEndpoint,
		gziphandler.GzipHandler(exposedMetricsMiddleware(listenAddr, metricsForwaders, promhttp.Handler())))
	go func() {
		log.Infoln(fmt.Sprintf("Starting server in port %d, path %s ...", listenPort, handlerEndpoint))
		log.WithError(srv.ListenAndServe()).Errorln("unable to start the server")
	}()
	return srv
}

// TODO(denisacostaq@gmail.com): you can use a NewProcessCollector, NewGoProcessCollector, make a blockchain collector sense?
