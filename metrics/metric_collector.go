// Licensed to the Apache Software Foundation (ASF) under one
// or more contributor license agreements.  See the NOTICE file
// distributed with this work for additional information
// regarding copyright ownership.  The ASF licenses this file
// to you under the Apache License, Version 2.0 (the
// "License"); you may not use this file except in compliance
// with the License.  You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package metrics

import (
	"errors"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"github.com/tidwall/gjson"
	"gopkg.in/tomb.v2"
)

const (
	MetaServer    int = 0
	ReplicaServer int = 1
)

type Metric struct {
	name string
	// For metric type for counter/gauge.
	value float64
	// For metric type of percentile.
	values []float64
	
	mtype  string
}

type Metrics []Metric

//针对p50,p90等级别定义层级
var TaskLevel = [5]string{"p50","p90","p95","p99","p999"}
var GaugeMetricsMap map[string]prometheus.GaugeVec
var CounterMetricsMap map[string]prometheus.CounterVec
var SummaryMetricsMap map[string]prometheus.Summary

// DataSource 0 meta server, 1 replica server.
//var DataSource int
var RoleByDataSource map[int]string

var TableNameByID map[string]string

type MetricCollector interface {
	Start(tom *tomb.Tomb) error
}

func NewMetricCollector(
	dataSource int,
	detectInterval time.Duration,
	detectTimeout time.Duration) MetricCollector {
	//DataSource = dataSource
	// GaugeMetricsMap = make(map[string]prometheus.GaugeVec, 128)
	// CounterMetricsMap = make(map[string]prometheus.CounterVec, 128)
	// SummaryMetricsMap = make(map[string]prometheus.Summary, 128)
	// RoleByDataSource = make(map[int]string, 128)
	// TableNameByID = make(map[string]string, 128)
	// RoleByDataSource[0] = "meta_server"
	// RoleByDataSource[1] = "replica_server"
	//initMetrics()

	//return &Collector{detectInterval: detectInterval, detectTimeout: detectTimeout}
	return &Collector{detectInterval: detectInterval, detectTimeout: detectTimeout,DataSource: dataSource}
}

type Collector struct {
	detectInterval time.Duration
	detectTimeout  time.Duration
	DataSource int
}

// type Collector struct {
// 	detectInterval time.Duration
// 	detectTimeout time.Duration

// 	CCounterMetricsMap map[string]prometheus.CounterVec
// 	CGaugeMetricsMap map[string]prometheus.GaugeVec
// 	SummaryMetricsMap map[string]prometheus.Summary
// }

func (collector *Collector) Start(tom *tomb.Tomb) error {
	ticker := time.NewTicker(collector.detectInterval)
	for {
		select {
		case <-tom.Dying():
			return nil
		case <-ticker.C:
			updateClusterTableInfo()

			//processAllServerMetrics()
			processAllServerMetrics(collector.DataSource)
		}
	}
}

// Get replica server address.
func getReplicaAddrs() ([]string, error) {
	addrs := viper.GetStringSlice("meta_servers")
	var rserverAddrs []string
	for _, addr := range addrs {
		url := fmt.Sprintf("http://%s/meta/nodes", addr)
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode != http.StatusOK {
			err = errors.New(resp.Status)
		}
		if err != nil {
			log.Errorf("Fail to get replica server address from %s, err %s", addr, err)
			continue
		}
		body, _ := ioutil.ReadAll(resp.Body)
		jsonData := gjson.Parse(string(body))
		for key := range jsonData.Get("details").Map() {
			rserverAddrs = append(rserverAddrs, key)
		}
		defer resp.Body.Close()
		break
	}
	return rserverAddrs, nil
}

//12-22，collector对象要分别实现describe,Collect函数
// func (collector *Collector) Describe(ch chan<- *prometheus.Desc) {

// 	strD := "The metrics of " + RoleByDataSource[DataSource];
// 	desc := prometheus.NewDesc(RoleByDataSource[DataSource],strD,nil,nil)

// 	ch <- desc
// }


// func (collector *Collector) Collect(ch chan<- prometheus.Metric) {
// 	for _ , counterVec := CounterMetricsMap {
// 		ch <- counterVec
// 	}
// 	for _ , gaugeVec := GaugeMetricsMap {
// 		ch <- gaugeVec
// 	}
// }

func getProMetricsByAddrs(addrs []string) {
	for _, addr := range addrs {
		data, err := getOneServerMetrics(addr)
		if err != nil {
			log.Errorf("Get raw metrics from %s failed, err: %s", addr, err)
			return
		}
		jsonData := gjson.Parse(data)
		for _, entity := range jsonData.Array() {
			for _, metric := range entity.Get("metrics").Array() {
				var name string = metric.Get("name").String()
				var mtype string = metric.Get("type").String()
				var desc string = metric.Get("desc").String()
				switch mtype {
				case "Counter":
					if _, ok := CounterMetricsMap[name]; ok {
						continue
					}
					counterMetric := promauto.NewCounterVec(prometheus.CounterOpts{
						Name: name,
						Help: desc,
					}, []string{"endpoint", "role", "level", "title"})
					CounterMetricsMap[name] = *counterMetric
				case "Gauge":
					if _, ok := GaugeMetricsMap[name]; ok {
						continue
					}
					gaugeMetric := promauto.NewGaugeVec(prometheus.GaugeOpts{
						Name: name,
						Help: desc,
					}, []string{"endpoint", "role", "level", "title"})
					GaugeMetricsMap[name] = *gaugeMetric
				case "Percentile":				//这个需要改动不能用这个表示,用gauge来表示分位数  --level(p50,p99),title(task_name)来替代区分
					if _, ok := GaugeMetricsMap[name]; ok {
						continue
					}
					gaugeMetric := promauto.NewGaugeVec(prometheus.GaugeOpts{
						Name: name,
						Help: desc,
					}, []string{"endpoint", "role", "level", "title"})
					GaugeMetricsMap[name] = *gaugeMetric
					// if _, ok := SummaryMetricsMap[name]; ok {
					// 	continue
					// }
					// summaryMetric := promauto.NewSummary(prometheus.SummaryOpts{
					// 	Name: name,
					// 	Help: desc,
					// 	Objectives: map[float64]float64{
					// 		0.5: 0.05, 0.9: 0.01, 0.95: 0.005, 0.99: 0.001, 0.999: 0.0001},
					// })
					// SummaryMetricsMap[name] = summaryMetric
				case "Histogram":
				default:
					log.Errorf("Unsupport metric type %s", mtype)
				}
			}
		}
	}
}

func InitMetrics() {

	GaugeMetricsMap = make(map[string]prometheus.GaugeVec, 256)
	CounterMetricsMap = make(map[string]prometheus.CounterVec, 256)
	SummaryMetricsMap = make(map[string]prometheus.Summary, 256)
	RoleByDataSource = make(map[int]string, 128)
	TableNameByID = make(map[string]string, 256)
	RoleByDataSource[0] = "meta_server"
	RoleByDataSource[1] = "replica_server"


	var addrs []string
	//var err error

	addrs = viper.GetStringSlice("meta_servers")

	replicAddrs,err := getReplicaAddrs()
	if(err != nil) {
		log.Errorf("Get raw metrics from %s failed, err: %s", replicAddrs, err)
 		return
	}

	addrs = append(addrs,replicAddrs...)
	
	getProMetricsByAddrs(addrs)
}

// Register all metrics.
//将初始化的代码抽出来，形成公共函数
// func initMetrics() {
// 	var addrs []string
// 	var err error
// 	if DataSource == MetaServer {
// 		addrs = viper.GetStringSlice("meta_servers")
// 	} else {
// 		addrs, err = getReplicaAddrs()
// 		if err != nil {
// 			log.Errorf("Get replica server address failed, err: %s", err)
// 			return
// 		}
// 	}
// 	for _, addr := range addrs {
// 		data, err := getOneServerMetrics(addr)
// 		if err != nil {
// 			log.Errorf("Get raw metrics from %s failed, err: %s", addr, err)
// 			return
// 		}
// 		jsonData := gjson.Parse(data)
// 		for _, entity := range jsonData.Array() {
// 			for _, metric := range entity.Get("metrics").Array() {
// 				var name string = metric.Get("name").String()
// 				var mtype string = metric.Get("type").String()
// 				var desc string = metric.Get("desc").String()
// 				switch mtype {
// 				case "Counter":
// 					if _, ok := CounterMetricsMap[name]; ok {
// 						continue
// 					}
// 					counterMetric := promauto.NewCounterVec(prometheus.CounterOpts{
// 						Name: name,
// 						Help: desc,
// 					}, []string{"endpoint", "role", "level", "title"})
// 					CounterMetricsMap[name] = *counterMetric
// 				case "Gauge":
// 					if _, ok := GaugeMetricsMap[name]; ok {
// 						continue
// 					}
// 					gaugeMetric := promauto.NewGaugeVec(prometheus.GaugeOpts{
// 						Name: name,
// 						Help: desc,
// 					}, []string{"endpoint", "role", "level", "title"})
// 					GaugeMetricsMap[name] = *gaugeMetric
// 				case "Percentile":
// 					if _, ok := SummaryMetricsMap[name]; ok {
// 						continue
// 					}
// 					summaryMetric := promauto.NewSummary(prometheus.SummaryOpts{
// 						Name: name,
// 						Help: desc,
// 						Objectives: map[float64]float64{
// 							0.5: 0.05, 0.9: 0.01, 0.95: 0.005, 0.99: 0.001, 0.999: 0.0001},
// 					})
// 					SummaryMetricsMap[name] = summaryMetric
// 				case "Histogram":
// 				default:
// 					log.Errorf("Unsupport metric type %s", mtype)
// 				}
// 			}
// 		}
// 	}
// }

// func processAllServerMetrics() {
// 	var addrs []string
// 	var err error
// 	if DataSource == MetaServer {
// 		addrs = viper.GetStringSlice("meta_servers")
// 	} else {
// 		addrs, err = getReplicaAddrs()
// 		if err != nil {
// 			log.Errorf("Get replica server address failed, err: %s", err)
// 			return
// 		}
// 	}
// 	// if dsource == MetaServer {
// 	// 	addrs = viper.GetStringSlice("meta_servers")
// 	// } else {
// 	// 	addrs, err = getReplicaAddrs()
// 	// 	if err != nil {
// 	// 		log.Errorf("Get replica server address failed, err: %s", err)
// 	// 		return
// 	// 	}
// 	// }
// 	metricsByTableID := make(map[string]Metrics, 128)
// 	metricsByServerTableID := make(map[string]Metrics, 128)
// 	var metricsOfCluster []Metric
// 	metricsByAddr := make(map[string]Metrics, 128)
// 	for _, addr := range addrs {
// 		data, err := getOneServerMetrics(addr)
// 		if err != nil {
// 			log.Errorf("failed to get data from %s, err %s", addr, err)
// 			return
// 		}
// 		jsonData := gjson.Parse(data)
// 		for _, entity := range jsonData.Array() {
// 			etype := entity.Get("type").String()
// 			switch etype {
// 			case "replica":
// 			case "partition":
// 				tableID := entity.Get("attributes").Get("table_id").String()
// 				mergeIntoClusterLevelTableMetric(entity.Get("metrics").Array(),
// 					tableID, &metricsByTableID)
// 			case "table":
// 				tableID := entity.Get("attributes").Get("table_id").String()
// 				mergeIntoClusterLevelTableMetric(entity.Get("metrics").Array(),
// 					tableID, &metricsByTableID)
// 				collectServerLevelTableMetric(entity.Get("metrics").Array(), tableID,
// 					&metricsByServerTableID)
// 				updateServerLevelTableMetrics(addr, metricsByServerTableID)
// 			case "server":
// 				mergeIntoClusterLevelServerMetric(entity.Get("metrics").Array(),
// 					metricsOfCluster)
// 				collectServerLevelServerMetrics(entity.Get("metrics").Array(),
// 					addr, &metricsByAddr)
// 			default:
// 				log.Errorf("Unsupport entity type %s", etype)
// 			}
// 		}
// 	}

// 	updateClusterLevelTableMetrics(metricsByTableID)
// 	updateServerLevelServerMetrics(metricsByAddr)
// 	updateClusterLevelMetrics(metricsOfCluster)
// }

// Parse metric data and update metrics.
func processAllServerMetrics(dsource int) {
	var addrs []string
	var err error
	// if DataSource == MetaServer {
	// 	addrs = viper.GetStringSlice("meta_servers")
	// } else {
	// 	addrs, err = getReplicaAddrs()
	// 	if err != nil {
	// 		log.Errorf("Get replica server address failed, err: %s", err)
	// 		return
	// 	}
	// }
	if dsource == MetaServer {
		addrs = viper.GetStringSlice("meta_servers")
	} else {
		addrs, err = getReplicaAddrs()
		if err != nil {
			log.Errorf("Get replica server address failed, err: %s", err)
			return
		}
	}
	metricsByTableID := make(map[string]Metrics, 128)
	metricsByServerTableID := make(map[string]Metrics, 128)
	metricsByTaskName := make(map[string]Metrics, 128)  	//针对profiler类型进行处理
	metricsOfReplicaByTableID := make(map[string]Metrics,128) 	//针对replica类型进行处理
	var metricsOfCluster []Metric
	metricsByAddr := make(map[string]Metrics, 128)
	for _, addr := range addrs {
		data, err := getOneServerMetrics(addr)
		if err != nil {
			log.Errorf("failed to get data from %s, err %s", addr, err)
			return
		}
		jsonData := gjson.Parse(data)
		for _, entity := range jsonData.Array() {
			etype := entity.Get("type").String()
			switch etype {
			case "replica":
				//工作待完成，这个大类里面有Counter,Gauge,Percentile类型指标  12-27
				tableID := entity.Get("attributes").Get("table_id").String()
				collectReplicaSeverLevelTableMetric(entity.Get("metrics").Array(),tableID,
					&metricsOfReplicaByTableID)
			case "profiler":
				taskName := entity.Get("attributes").Get("task_name").String()
				mergeIntoServerLevelTaskMetrics(entity.Get("metrics").Array(),
					taskName,&metricsByTaskName)
				updateServerLevelTaskMetrics(addr,metricsByTaskName,dsource)
			case "partition":
				tableID := entity.Get("attributes").Get("table_id").String()
				mergeIntoClusterLevelTableMetric(entity.Get("metrics").Array(),
					tableID, &metricsByTableID)
				//updateServerLevelTaskMetrics(addr,metricsByTaskName,dsource)
			case "table":
				tableID := entity.Get("attributes").Get("table_id").String()
				mergeIntoClusterLevelTableMetric(entity.Get("metrics").Array(),
					tableID, &metricsByTableID)
				collectServerLevelTableMetric(entity.Get("metrics").Array(), tableID,
					&metricsByServerTableID)
				updateServerLevelTableMetrics(addr, metricsByServerTableID,dsource)
			case "server":
				mergeIntoClusterLevelServerMetric(entity.Get("metrics").Array(),
					metricsOfCluster)
				collectServerLevelServerMetrics(entity.Get("metrics").Array(),
					addr, &metricsByAddr)
			default:
				log.Errorf("Unsupport entity type %s", etype)
			}
		}

		//replica分片是replica-server才具有的，所有指标汇总完成之后进行prometheus数据更新，这个位置放置是否合理后面可能要进行确认修改
		updateReplicaServerLevelTableMetric(addr,metricsOfReplicaByTableID,dsource)
	}

	//updateServerLevelTaskMetrics(addr,metricsByTaskName,dsource)
	updateClusterLevelTableMetrics(metricsByTableID,dsource)
	updateServerLevelServerMetrics(metricsByAddr,dsource)
	updateClusterLevelMetrics(metricsOfCluster,dsource)
}

//将replica数据更新到prometheus集合中，方便后面采集
func updateReplicaServerLevelTableMetric(addr string,metricsOfReplicaByTableID map[string]Metrics,dsource int) {
	for tableID, metrics := range metricsOfReplicaByTableID {
		var tableName string
		if name, ok := TableNameByID[tableID]; !ok {
			tableName = tableID
		}else {
			tableName = name
		}

		for _, metric := range metrics {
			mtype := metric.mtype
			switch mtype {
			case "Counter":
				updateMetric(metric, addr, "table", tableName,dsource)
			case "Gauge":
				updateMetric(metric, addr, "table", tableName,dsource)
			case "Percentile":
				updateTaskMetric(metric,addr,tableName,dsource)
			default:
				log.Warnf("Unsupport metric type %s", metric.mtype)
			}
		}
	}
}

//将profiler数据更新到prometheus集合中，方便后面采集
func updateServerLevelTaskMetrics(addr string,metricsByTaskName map[string]Metrics,dsource int) {
	for taskName, metrics := range metricsByTaskName {
		//实际的更新操作
		for _, metric := range metrics {
			updateTaskMetric(metric,addr,taskName,dsource)
		}
	}
}

//新实现的针对profile指标更新到prometheus中的函数
func updateTaskMetric(metric Metric,endpoint string,title string,dsource int) {
	role := RoleByDataSource[dsource]
	switch metric.mtype {
	case "Counter":
		if counter, ok := CounterMetricsMap[metric.name]; ok {
			counter.With(
				prometheus.Labels{"endpoint": endpoint,
					"role": role, "level": "Task",
					"title": title}).Add(float64(metric.value))
		} else {
			log.Warnf("Unknown metric name %s", metric.name)
		}
	case "Gauge":
		if gauge, ok := GaugeMetricsMap[metric.name]; ok {
			gauge.With(
				prometheus.Labels{"endpoint": endpoint,
					"role": role, "level": "Task",
					"title": title}).Set(float64(metric.value))
		} else {
			log.Warnf("Unknown metric name %s", metric.name)
		}
	case "Percentile":
		//log.Warnf("Todo metric type %s", metric.mtype)
		if gauge, ok := GaugeMetricsMap[metric.name]; ok {
			//各个级别的数据依次更新
			for i := 0; i < 5; i++ {
				gauge.With(
					prometheus.Labels{"endpoint": endpoint,
						"role": role, "level": TaskLevel[i],
						"title": title}).Set(float64(metric.values[i]))
			}
			//fmt.Printf("values[0]:%f \n",metric.values[0])
			// if(len(metric.values) == 0) {
			// 	break
			// }
			// p := float64(metric.values[0])
			// gauge.With(
			// 	prometheus.Labels{"endpoint": endpoint,
			// 		"role": role, "level": "p50",
			// 		"title": title}).Set(p)
			// p = float64(metric.values[1])
			// gauge.With(
			// 	prometheus.Labels{"endpoint": endpoint,
			// 		"role": role, "level": "p90",
			// 		"title": title}).Set(p)
			// p = float64(metric.values[2])
			// gauge.With(
			// 	prometheus.Labels{"endpoint": endpoint, 
			// 		"role": role, "level": "p95",
			// 		"title": title}).Set(p)
			// p = float64(metric.values[3])
			// gauge.With(
			// 	prometheus.Labels{"endpoint": endpoint,
			// 		"role": role, "level": "p99",
			// 		"title": title}).Set(p)
			// p = float64(metric.values[4])
			// gauge.With(
			// 	prometheus.Labels{"endpoint": endpoint,
			// 		"role": role, "level": "p999",
			// 		"title": title}).Set(p)
		}else {
			log.Warnf("Unknown metric name %s", metric.name)
		}
	case "Histogram":
	default:
		log.Warnf("Unsupport metric type %s", metric.mtype)
	}
}


// Update table metrics. They belong to a specified server.
func updateServerLevelTableMetrics(addr string, metricsByServerTableID map[string]Metrics,dsource int) {
	for tableID, metrics := range metricsByServerTableID {
		var tableName string
		if name, ok := TableNameByID[tableID]; !ok {
			tableName = tableID
		} else {
			tableName = name
		}
		for _, metric := range metrics {
			updateMetric(metric, addr, "server", tableName,dsource)
		}
	}
}

// Update server metrics. They belong to a specified server.
func updateServerLevelServerMetrics(metricsByAddr map[string]Metrics,dsource int) {
	for addr, metrics := range metricsByAddr {
		for _, metric := range metrics {
			updateMetric(metric, addr, "server", "server",dsource)
		}
	}
}

// Update cluster level metrics. They belong to a cluster.
func updateClusterLevelMetrics(metricsOfCluster []Metric,dsource int) {
	for _, metric := range metricsOfCluster {
		updateMetric(metric, "cluster", "server", metric.name,dsource)
	}
}

// Update table metrics. They belong to a cluster.
func updateClusterLevelTableMetrics(metricsByTableID map[string]Metrics,dsource int) {
	for tableID, metrics := range metricsByTableID {
		var tableName string
		if name, ok := TableNameByID[tableID]; !ok {
			tableName = tableID
		} else {
			tableName = name
		}
		for _, metric := range metrics {
			updateMetric(metric, "cluster", "table", tableName,dsource)
		}
	}
}

func updateMetric(metric Metric, endpoint string, level string, title string,dsource int) {
	role := RoleByDataSource[dsource]
	switch metric.mtype {
	case "Counter":
		if counter, ok := CounterMetricsMap[metric.name]; ok {
			counter.With(
				prometheus.Labels{"endpoint": endpoint,
					"role": role, "level": level,
					"title": title}).Add(float64(metric.value))
		} else {
			log.Warnf("Unknown metric name %s", metric.name)
		}
	case "Gauge":
		if gauge, ok := GaugeMetricsMap[metric.name]; ok {
			gauge.With(
				prometheus.Labels{"endpoint": endpoint,
					"role": role, "level": level,
					"title": title}).Set(float64(metric.value))
		} else {
			log.Warnf("Unknown metric name %s", metric.name)
		}
	case "Percentile":
		log.Warnf("Todo metric type %s", metric.mtype)
	case "Histogram":
	default:
		log.Warnf("Unsupport metric type %s", metric.mtype)
	}
}

func collectServerLevelTableMetric(metrics []gjson.Result, tableID string,
	metricsByServerTableID *map[string]Metrics) {
	var mts Metrics
	for _, metric := range metrics {
		name := metric.Get("name").String()
		mtype := metric.Get("type").String()
		value := metric.Get("value").Float()
		var values []float64
		if mtype == "Percentile" {
			values = append(values, metric.Get("p50").Float())
			values = append(values, metric.Get("p90").Float())
			values = append(values, metric.Get("p95").Float())
			values = append(values, metric.Get("p99").Float())
			values = append(values, metric.Get("p999").Float())
		}
		m := Metric{name: name, mtype: mtype, value: value, values: values}
		mts = append(mts, m)
	}
	(*metricsByServerTableID)[tableID] = mts
}

func collectServerLevelServerMetrics(metrics []gjson.Result, addr string,
	metricsByAddr *map[string]Metrics) {
	var mts Metrics
	for _, metric := range metrics {
		name := metric.Get("name").String()
		mtype := metric.Get("type").String()
		value := metric.Get("value").Float()
		var values []float64
		if mtype == "Percentile" {

			values = append(values, metric.Get("p50").Float())
			values = append(values, metric.Get("p90").Float())
			values = append(values, metric.Get("p95").Float())
			values = append(values, metric.Get("p99").Float())
			values = append(values, metric.Get("p999").Float())
		}
		m := Metric{name: name, mtype: mtype, value: value, values: values}
		mts = append(mts, m)
	}
	(*metricsByAddr)[addr] = mts
}

//周期性地将获取的指标存储并更新到prometheus接口数据
func collectReplicaSeverLevelTableMetric(metrics []gjson.Result,tableID string,
	metricsOfReplicaByTableID *map[string]Metrics) {
		if _, ok := (*metricsOfReplicaByTableID)[tableID]; ok {
			//已经存在进行更新
			mts := (*metricsOfReplicaByTableID)[tableID]
			for _, metric := range metrics {
				name := metric.Get("name").String()
				mtype := metric.Get("type").String()
				value := metric.Get("value").Float()
				for _, m := range mts {
					if name == m.name {
						switch mtype {
						case "Counter":
							m.value += value			//同一个分片的不同replica如何进行指标的汇聚操作？？？？？？？
						case "Gauge":
							m.value += value
						case "Percentile":
							p50 := metric.Get("p50").Float()
							m.values[0] = math.Max(m.values[0], p50)		//这个地方的数据更新好像也有问题？？？？ -------> 后面可能要修改
							p90 := metric.Get("p90").Float()
							m.values[1] = math.Max(m.values[0], p90)
							p95 := metric.Get("p95").Float()
							m.values[2] = math.Max(m.values[0], p95)
							p99 := metric.Get("p99").Float()
							m.values[3] = math.Max(m.values[0], p99)
							p999 := metric.Get("p999").Float()
							m.values[4] = math.Max(m.values[0], p999)
						case "Histogram":
						default:
							log.Errorf("Unsupport metric type %s", mtype)
						}
					}
				}
			}
		} else {
			var mts Metrics
			for _, metric := range metrics {
				name := metric.Get("name").String()
				mtype := metric.Get("type").String()
				value := metric.Get("value").Float()
				var values []float64
				if mtype == "Percentile" {
					values = append(values, metric.Get("p50").Float())
					values = append(values, metric.Get("p90").Float())
					values = append(values, metric.Get("p95").Float())
					values = append(values, metric.Get("p99").Float())
					values = append(values, metric.Get("p999").Float())
				}
				m := Metric{name: name, mtype: mtype, value: value, values: values}
				mts = append(mts, m)
			}
			(*metricsOfReplicaByTableID)[tableID] = mts
		}
}

//profiler的percentile类型数据合并收集并存
func mergeIntoServerLevelTaskMetrics(metrics []gjson.Result, taskName string,
	metricsByTaskName *map[string]Metrics) {
		if _, ok := (*metricsByTaskName)[taskName]; ok {
			mts := (*metricsByTaskName)[taskName]
			for _, metric := range metrics {
				name := metric.Get("name").String()
				mtype := metric.Get("type").String()
				value := metric.Get("value").Float()
				for _, m := range mts {
					if name == m.name {
						switch mtype {
						case "Counter":
							m.value = value				//这么处理不知道正确？
						case "Gauge":
							m.value += value
						case "Percentile":				//percentile类型的更新是否正确？
							// if(len(m.values) == 0) {
							// 	break
							// }
							p50 := metric.Get("p50").Float()
							m.values[0] = math.Max(m.values[0], p50)
							p90 := metric.Get("p90").Float()
							m.values[1] = math.Max(m.values[0], p90)
							p95 := metric.Get("p95").Float()
							m.values[2] = math.Max(m.values[0], p95)
							p99 := metric.Get("p99").Float()
							m.values[3] = math.Max(m.values[0], p99)
							p999 := metric.Get("p999").Float()
							m.values[4] = math.Max(m.values[0], p999)
						case "Histogram":
						default:
							log.Errorf("Unsupport metric type %s", mtype)
						}
					}
				}
			}
		} else {
			var mts Metrics
			for _, metric := range metrics {
				name := metric.Get("name").String()
				mtype := metric.Get("type").String()
				value := metric.Get("value").Float()

				//m := Metric{name: name, mtype: mtype, value: value}
				var values []float64
				if mtype == "Percentile" {
					values = append(values, metric.Get("p50").Float())
					values = append(values, metric.Get("p90").Float())
					values = append(values, metric.Get("p95").Float())
					values = append(values, metric.Get("p99").Float())
					values = append(values, metric.Get("p999").Float())
				}
				//copy(m.values,values)
				m := Metric{name: name, mtype: mtype, value: value, values: values}
				mts = append(mts, m)
			}
			(*metricsByTaskName)[taskName] = mts
		}
}

func mergeIntoClusterLevelServerMetric(metrics []gjson.Result, metricsOfCluster []Metric) {
	for _, metric := range metrics {
		name := metric.Get("name").String()
		mtype := metric.Get("type").String()
		value := metric.Get("value").Float()
		var isExisted bool = false
		for _, m := range metricsOfCluster {
			if m.name == name {
				isExisted = true
				switch mtype {
				case "Counter":
				case "Gauge":
					m.value += value
				case "Percentile":
					p50 := metric.Get("p50").Float()
					m.values[0] = math.Max(m.values[0], p50)
					p90 := metric.Get("p90").Float()
					m.values[1] = math.Max(m.values[0], p90)
					p95 := metric.Get("p95").Float()
					m.values[2] = math.Max(m.values[0], p95)
					p99 := metric.Get("p99").Float()
					m.values[3] = math.Max(m.values[0], p99)
					p999 := metric.Get("p999").Float()
					m.values[4] = math.Max(m.values[0], p999)
				case "Histogram":
				default:
					log.Errorf("Unsupport metric type %s", mtype)
				}
			}
		}
		if !isExisted {
			value := metric.Get("value").Float()
			var values []float64
			if mtype == "Percentile" {
				values = append(values, metric.Get("p50").Float())
				values = append(values, metric.Get("p90").Float())
				values = append(values, metric.Get("p95").Float())
				values = append(values, metric.Get("p99").Float())
				values = append(values, metric.Get("p999").Float())
			}
			m := Metric{name: name, mtype: mtype, value: value, values: values}
			metricsOfCluster = append(metricsOfCluster, m)
		}
	}
}


func mergeIntoClusterLevelTableMetric(metrics []gjson.Result, tableID string,
	metricsByTableID *map[string]Metrics) {
	// Find a same table id, try to merge them.
	if _, ok := (*metricsByTableID)[tableID]; ok {
		mts := (*metricsByTableID)[tableID]
		for _, metric := range metrics {
			name := metric.Get("name").String()
			mtype := metric.Get("type").String()
			value := metric.Get("value").Float()
			for _, m := range mts {
				if name == m.name {
					switch mtype {
					case "Counter":
					case "Gauge":
						m.value += value
					case "Percentile":
						p50 := metric.Get("p50").Float()
						m.values[0] = math.Max(m.values[0], p50)
						p90 := metric.Get("p90").Float()
						m.values[1] = math.Max(m.values[0], p90)
						p95 := metric.Get("p95").Float()
						m.values[2] = math.Max(m.values[0], p95)
						p99 := metric.Get("p99").Float()
						m.values[3] = math.Max(m.values[0], p99)
						p999 := metric.Get("p999").Float()
						m.values[4] = math.Max(m.values[0], p999)
					case "Histogram":
					default:
						log.Errorf("Unsupport metric type %s", mtype)
					}
				}
			}
		}
	} else {
		var mts Metrics
		for _, metric := range metrics {
			name := metric.Get("name").String()
			mtype := metric.Get("type").String()
			value := metric.Get("value").Float()
			var values []float64
			if mtype == "Percentile" {
				values = append(values, metric.Get("p50").Float())
				values = append(values, metric.Get("p90").Float())
				values = append(values, metric.Get("p95").Float())
				values = append(values, metric.Get("p99").Float())
				values = append(values, metric.Get("p999").Float())
			}
			m := Metric{name: name, mtype: mtype, value: value, values: values}
			mts = append(mts, m)
		}
		(*metricsByTableID)[tableID] = mts
	}
}

func getOneServerMetrics(addr string) (string, error) {
	url := fmt.Sprintf("http://%s/metrics?detail=true", addr)
	return httpGet(url)
}

func httpGet(url string) (string, error) {
	resp, err := http.Get(url)
	if err == nil && resp.StatusCode != http.StatusOK {
		err = errors.New(resp.Status)
	}
	if err != nil {
		log.Errorf("Fail to get data from %s, err %s", url, err)
		return "", err
	}
	body, _ := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()
	return string(body), nil
}

//集群数据，记录主节点编号
func getClusterInfo() (string, error) {
	addrs := viper.GetStringSlice("meta_servers")
	url := fmt.Sprintf("http://%s/meta/cluster", addrs[0])
	return httpGet(url)
}

func getTableInfo(pMetaServer string) (string, error) {
	url := fmt.Sprintf("http://%s/meta/apps", pMetaServer)
	return httpGet(url)
}

//更新集群的所有表的信息 --- id name
func updateClusterTableInfo() {
	// Get primary meta server address.
	data, err := getClusterInfo()
	if err != nil {
		log.Error("Fail to get cluster info")
		return
	}
	jsonData := gjson.Parse(data)
	pMetaServer := jsonData.Get("primary_meta_server").String()
	data, err = getTableInfo(pMetaServer)
	if err != nil {
		log.Error("Fail to get table info")
		return
	}
	jsonData = gjson.Parse(data)
	for _, value := range jsonData.Get("general_info").Map() {
		tableID := value.Get("app_id").String()
		tableName := value.Get("app_name").String()
		if _, ok := TableNameByID[tableID]; !ok {
			TableNameByID[tableID] = tableName
		}
	}
}
