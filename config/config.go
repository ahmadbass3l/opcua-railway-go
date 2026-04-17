package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	OpcuaEndpoint   string
	OpcuaNodeIDs    []string
	OpcuaIntervalMs uint32
	DBDSN           string
	Port            string
}

func Load() Config {
	nodeIDsRaw := getEnv("OPCUA_NODE_IDS", "ns=2;i=1001,ns=2;i=1002,ns=2;i=1003")
	nodeIDs := []string{}
	for _, s := range strings.Split(nodeIDsRaw, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			nodeIDs = append(nodeIDs, s)
		}
	}

	intervalMs := uint32(500)
	if v := os.Getenv("OPCUA_INTERVAL_MS"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			intervalMs = uint32(n)
		}
	}

	return Config{
		OpcuaEndpoint:   getEnv("OPCUA_ENDPOINT", "opc.tcp://localhost:4840"),
		OpcuaNodeIDs:    nodeIDs,
		OpcuaIntervalMs: intervalMs,
		DBDSN:           getEnv("DB_DSN", "postgresql://railway:railway@localhost:5432/railway"),
		Port:            getEnv("PORT", "8080"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
