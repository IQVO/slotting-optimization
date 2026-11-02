package config

import (
	"errors"
	"strings"
	"testing"
)

// validEnv is the smallest environment the binary boots with; tests delete or
// overwrite single keys of it.
func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://u@db:5432/x", "KAFKA_BROKERS": "a:9092,b:9092",
		"DEMAND_CONSUMER_GROUP": "g-demand", "PRODUCT_CONSUMER_GROUP": "g-product",
		"LAYOUT_CONSUMER_GROUP": "g-layout", "RELOCATION_CONSUMER_GROUP": "g-relocation",
		"PACK_LEVEL_CONSUMER_GROUP": "g-pack-level",
	}
}

func withEnv(overrides map[string]string) func(string) string {
	env := validEnv()
	for k, v := range overrides {
		if v == "-" {
			delete(env, k)
			continue
		}
		env[k] = v
	}
	return func(k string) string { return env[k] }
}

// testConsumers mirrors the composition root's five local-copy consumers.
func testConsumers() []ConsumerEnv {
	return []ConsumerEnv{
		{"DEMAND_MODE", "DEMAND_CONSUMER_GROUP"},
		{"PRODUCT_MODE", "PRODUCT_CONSUMER_GROUP"},
		{"LAYOUT_MODE", "LAYOUT_CONSUMER_GROUP"},
		{"RELOCATION_MODE", "RELOCATION_CONSUMER_GROUP"},
		{"PACK_LEVEL_MODE", "PACK_LEVEL_CONSUMER_GROUP"},
	}
}

func TestLoadRuntimeAcceptsTheMinimalRealEnvironment(t *testing.T) {
	rt, err := LoadRuntime(withEnv(nil), testConsumers())
	if err != nil || len(rt.Brokers) != 2 || rt.Brokers[1] != "b:9092" {
		t.Fatalf("rt = %+v, err = %v", rt, err)
	}
	for env, want := range map[string]string{"DEMAND_CONSUMER_GROUP": "g-demand", "PRODUCT_CONSUMER_GROUP": "g-product", "LAYOUT_CONSUMER_GROUP": "g-layout", "RELOCATION_CONSUMER_GROUP": "g-relocation", "PACK_LEVEL_CONSUMER_GROUP": "g-pack-level"} {
		if rt.Groups[env] != want {
			t.Errorf("group %s = %q, want %q", env, rt.Groups[env], want)
		}
	}
	// The explicit values the chart renders are accepted too.
	explicit := map[string]string{"DEMAND_MODE": "kafka", "PRODUCT_MODE": "kafka", "LAYOUT_MODE": "kafka", "RELOCATION_MODE": "kafka", "PACK_LEVEL_MODE": "kafka", "EVENT_PUBLISHER": "kafka"}
	if _, err := LoadRuntime(withEnv(explicit), testConsumers()); err != nil {
		t.Fatalf("explicit kafka modes: %v", err)
	}
}

// Every refused environment is a boot error whose message names the variable
// (ADR 0011), raised by LoadRuntime before anything is opened or bound.
func TestLoadRuntimeRefusesWithAnErrorNamingTheVariable(t *testing.T) {
	cases := []struct {
		name     string
		override map[string]string
		wantVar  string
	}{
		{"no DATABASE_URL", map[string]string{"DATABASE_URL": "-"}, "DATABASE_URL"},
		{"no KAFKA_BROKERS", map[string]string{"KAFKA_BROKERS": "-"}, "KAFKA_BROKERS"},
		{"blank KAFKA_BROKERS", map[string]string{"KAFKA_BROKERS": " , "}, "KAFKA_BROKERS"},
		{"no DEMAND_CONSUMER_GROUP", map[string]string{"DEMAND_CONSUMER_GROUP": "-"}, "DEMAND_CONSUMER_GROUP"},
		{"no PRODUCT_CONSUMER_GROUP", map[string]string{"PRODUCT_CONSUMER_GROUP": "-"}, "PRODUCT_CONSUMER_GROUP"},
		{"no LAYOUT_CONSUMER_GROUP", map[string]string{"LAYOUT_CONSUMER_GROUP": "-"}, "LAYOUT_CONSUMER_GROUP"},
		{"no RELOCATION_CONSUMER_GROUP", map[string]string{"RELOCATION_CONSUMER_GROUP": "-"}, "RELOCATION_CONSUMER_GROUP"},
		{"no PACK_LEVEL_CONSUMER_GROUP", map[string]string{"PACK_LEVEL_CONSUMER_GROUP": "-"}, "PACK_LEVEL_CONSUMER_GROUP"},
		{"PACK_LEVEL_MODE=permissive", map[string]string{"PACK_LEVEL_MODE": "permissive"}, "PACK_LEVEL_MODE=permissive is not supported"},
		{"PACK_LEVEL_MODE=off", map[string]string{"PACK_LEVEL_MODE": "off"}, "PACK_LEVEL_MODE=off is not supported"},
		{"DEMAND_MODE=permissive", map[string]string{"DEMAND_MODE": "permissive"}, "DEMAND_MODE=permissive is not supported"},
		{"PRODUCT_MODE=permissive", map[string]string{"PRODUCT_MODE": "permissive"}, "PRODUCT_MODE=permissive is not supported"},
		{"LAYOUT_MODE=permissive", map[string]string{"LAYOUT_MODE": "permissive"}, "LAYOUT_MODE=permissive is not supported"},
		{"RELOCATION_MODE=permissive", map[string]string{"RELOCATION_MODE": "permissive"}, "RELOCATION_MODE=permissive is not supported"},
		{"DEMAND_MODE=off", map[string]string{"DEMAND_MODE": "off"}, "DEMAND_MODE=off is not supported"},
		{"RELOCATION_MODE=off", map[string]string{"RELOCATION_MODE": "off"}, "RELOCATION_MODE=off is not supported"},
		{"unknown LAYOUT_MODE", map[string]string{"LAYOUT_MODE": "http"}, "LAYOUT_MODE"},
		{"EVENT_PUBLISHER=log", map[string]string{"EVENT_PUBLISHER": "log"}, "EVENT_PUBLISHER=log is not supported"},
		{"unknown EVENT_PUBLISHER", map[string]string{"EVENT_PUBLISHER": "rabbit"}, "EVENT_PUBLISHER"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadRuntime(withEnv(tc.override), testConsumers())
			if err == nil || !strings.Contains(err.Error(), tc.wantVar) {
				t.Fatalf("err = %v, want a boot error containing %q", err, tc.wantVar)
			}
		})
	}
}

func TestLoadRuntimePermissiveMessageNamesRealKafka(t *testing.T) {
	_, err := LoadRuntime(withEnv(map[string]string{"DEMAND_MODE": "permissive"}), testConsumers())
	if err == nil || err.Error() != "DEMAND_MODE=permissive is not supported: this context runs on real Kafka" {
		t.Fatalf("err = %v", err)
	}
}

// A completely empty environment fails on the first mandatory variable.
func TestLoadRuntimeEmptyEnvIsABootError(t *testing.T) {
	if _, err := LoadRuntime(func(string) string { return "" }, testConsumers()); !errors.Is(err, ErrMissingDatabaseURL) {
		t.Fatalf("LoadRuntime(empty) = %v, want ErrMissingDatabaseURL", err)
	}
}
