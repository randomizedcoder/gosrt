package common

import (
	"encoding/json"
	"fmt"
	"os"

	srt "github.com/randomizedcoder/gosrt"
)

// HandleTestFlags checks if testflags mode is active. If so, it creates a
// default config, applies CLI flags, marshals to JSON, prints to stdout,
// and returns (exitCode, true). If testflags is not active, returns (0, false).
// The configModifier callback allows binaries to apply additional config
// changes (e.g., server-specific passphrase override) before marshaling.
func HandleTestFlags(testflags bool, configModifier func(*srt.Config)) (exitCode int, handled bool) {
	if !testflags {
		return 0, false
	}

	config := srt.DefaultConfig()
	ApplyFlagsToConfig(&config)

	if configModifier != nil {
		configModifier(&config)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling config: %v\n", err)
		return 1, true
	}
	fmt.Println(string(data))
	return 0, true
}
