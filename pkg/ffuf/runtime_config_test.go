package ffuf

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml"
)

func runtimeConfigOptions() *ConfigOptions {
	opts := NewConfigOptions()
	opts.HTTP.URL = "https://example.org/FUZZ"
	opts.Input.Wordlists = []string{"/tmp/words.txt"}
	return opts
}

func TestRuntimeConfigFieldsAreNotSerialized(t *testing.T) {
	defaultConf, err := ConfigFromOptions(runtimeConfigOptions(), context.Background(), func() {})
	if err != nil {
		t.Fatalf("ConfigFromOptions with default threads: %v", err)
	}
	if defaultConf.ThreadsExplicit {
		t.Fatal("default thread count was marked explicit")
	}

	opts := runtimeConfigOptions()
	opts.General.Threads = defaultThreadCount + 1
	conf, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err != nil {
		t.Fatalf("ConfigFromOptions with non-default threads: %v", err)
	}
	if conf.Threads != defaultThreadCount+1 || !conf.ThreadsExplicit {
		t.Fatalf("non-default thread mapping = (%d, %t), want (%d, true)", conf.Threads, conf.ThreadsExplicit, defaultThreadCount+1)
	}

	conf.Http3Selected = true
	conf.Http3ConnectionsActive = 17
	conf.ResponseOptimizationStrategy = "head"
	conf.ResponseOptimizationReason = "reliable empty-body baseline"

	runtimeFields := []string{
		"ThreadsExplicit",
		"Http3MaxWorkersExplicit",
		"Http3Selected",
		"Http3ConnectionsActive",
		"ResponseOptimizationStrategy",
		"ResponseOptimizationReason",
	}
	assertRuntimeTags(t, runtimeFields)

	configJSON, err := json.Marshal(conf)
	if err != nil {
		t.Fatalf("marshal Config as JSON: %v", err)
	}
	assertRuntimeFieldsAbsent(t, "Config JSON", configJSON)

	historyJSON, err := json.Marshal(struct {
		ConfigOptions
		Time string `json:"time"`
	}{ConfigOptions: *conf.Options, Time: "test"})
	if err != nil {
		t.Fatalf("marshal history-compatible options: %v", err)
	}
	assertRuntimeFieldsAbsent(t, "history JSON", historyJSON)

	tomlProjection := runtimeFieldProjection(t, *conf, runtimeFields)
	tomlData, err := toml.Marshal(tomlProjection)
	if err != nil {
		t.Fatalf("marshal runtime-field TOML projection: %v", err)
	}
	assertRuntimeFieldsAbsent(t, "Config TOML", tomlData)
}

func assertRuntimeTags(t *testing.T, names []string) {
	t.Helper()
	typ := reflect.TypeOf(Config{})
	for _, name := range names {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Fatalf("Config field %s is missing", name)
		}
		if field.Tag.Get("json") != "-" || field.Tag.Get("toml") != "-" {
			t.Errorf("Config.%s tags = %q, want json and toml omitted", name, field.Tag)
		}
	}
}

func runtimeFieldProjection(t *testing.T, conf Config, names []string) any {
	t.Helper()
	configType := reflect.TypeOf(conf)
	configValue := reflect.ValueOf(conf)
	fields := make([]reflect.StructField, 0, len(names))
	for _, name := range names {
		field, ok := configType.FieldByName(name)
		if !ok {
			t.Fatalf("Config field %s is missing", name)
		}
		fields = append(fields, reflect.StructField{Name: field.Name, Type: field.Type, Tag: field.Tag})
	}

	projection := reflect.New(reflect.StructOf(fields)).Elem()
	for i, name := range names {
		projection.Field(i).Set(configValue.FieldByName(name))
	}
	return projection.Interface()
}

func assertRuntimeFieldsAbsent(t *testing.T, representation string, data []byte) {
	t.Helper()
	normalized := strings.ToLower(string(data))
	for _, forbidden := range []string{
		"threadsexplicit", "threads_explicit",
		"http3selected", "http3_selected",
		"http3connectionsactive", "http3_connections_active",
		"responseoptimizationstrategy", "response_optimization_strategy",
		"responseoptimizationreason", "response_optimization_reason",
	} {
		if strings.Contains(normalized, forbidden) {
			t.Errorf("%s unexpectedly contains runtime field %q: %s", representation, forbidden, data)
		}
	}
}
