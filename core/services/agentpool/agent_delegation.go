// SPDX-License-Identifier: MIT
package agentpool

import (
	"strings"

	"github.com/mudler/LocalAGI/core/types"
)

func resolvePoolSubAgent(parent string, _ *types.Job, candidate string) (string, bool) {
	// Pool keys are trusted; job metadata must not grant access to another owner.
	// Agent names cannot contain colons, but user IDs (including API-key IDs) can.
	owner := func(key string) (string, string) {
		if i := strings.LastIndexByte(key, ':'); i >= 0 {
			return key[:i], key[i+1:]
		}
		return "", key
	}
	parentOwner, _ := owner(parent)
	candidateOwner, name := owner(candidate)
	if parentOwner != candidateOwner || name == "" {
		return "", false
	}
	return name, true
}
