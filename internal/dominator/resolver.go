package dominator

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"log"
	"os"

	"github.com/nabutabu/crane-oss/pkg/api"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
)

type PolicyResolver struct {
	program *starlark.Program
}

func NewPolicyResolver(path string) (*PolicyResolver, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	_, prog, err := starlark.SourceProgramOptions(&syntax.FileOptions{
		While: true,
	}, path, src, nil)
	if err != nil {
		return nil, err
	}

	return &PolicyResolver{
		program: prog,
	}, nil
}

func starlarkstructFromDict(dict starlark.StringDict) *starlarkstruct.Struct {
	return starlarkstruct.FromStringDict(
		starlark.String("host"),
		dict,
	)
}

func computeBucket(hostID string) int {
	hash := sha1.Sum([]byte(hostID))
	num := binary.BigEndian.Uint32(hash[:4])
	return int(num % 100)
}

func starlarkResultToDesiredState(val starlark.Value) (api.DesiredState, error) {
	dict, ok := val.(*starlark.Dict)
	if !ok {
		return api.DesiredState{}, fmt.Errorf("policy must return dict")
	}

	get := func(key string) (string, error) {
		v, _, err := dict.Get(starlark.String(key))
		if err != nil || v == nil {
			return "", fmt.Errorf("missing key: %s", key)
		}
		return string(v.(starlark.String)), nil
	}

	imageID, err := get("image_id")
	if err != nil {
		return api.DesiredState{}, err
	}

	track, err := get("track")
	if err != nil {
		return api.DesiredState{}, err
	}

	version, err := get("version")
	if err != nil {
		return api.DesiredState{}, err
	}

	return api.DesiredState{
		ImageID: imageID,
		Track:   track,
		Version: version,
	}, nil
}

func (r *PolicyResolver) Resolve(host *api.Host) (api.DesiredState, error) {
	thread := &starlark.Thread{Name: "dominator"}

	globals, err := r.program.Init(thread, nil)
	if err != nil {
		return api.DesiredState{}, err
	}

	resolveFn, ok := globals["resolve"] // the name of the function in the file path provided to constructor
	if !ok {
		return api.DesiredState{}, fmt.Errorf("resolve() not defined in policy")
	}

	hostDict := starlark.StringDict{
		"id":     starlark.String(host.ID),
		"zone":   starlark.String(host.Zone),
		"role":   starlark.String(host.Role.ExpectedImage),
		"bucket": starlark.MakeInt(computeBucket(host.ID)),
	}

	hostStruct := starlarkstructFromDict(hostDict)

	result, err := starlark.Call(thread, resolveFn, starlark.Tuple{hostStruct}, nil)
	if err != nil {
		return api.DesiredState{}, err
	}

	log.Printf("Sending following desired state to subd: %s", result)
	return starlarkResultToDesiredState(result)
}
