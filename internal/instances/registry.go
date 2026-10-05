package instances

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/truepace-io-oss/gitlab-mcp-server/internal/config"
)

// Registry is a thread-safe collection of the GitLab instances this MCP manages.
type Registry struct {
	mu          sync.RWMutex
	instances   map[string]*Instance
	order       []string
	defaultName string
}

// Build constructs a Registry from config. Building an instance's client does not
// contact GitLab, so an unreachable instance does not fail Build; reachability is
// reported later via Instance.Ping and the instances_list tool.
func Build(cfg *config.Config) (*Registry, error) {
	r := &Registry{
		instances:   make(map[string]*Instance, len(cfg.Instances)),
		defaultName: cfg.DefaultInstance,
	}
	for _, ic := range cfg.Instances {
		in, err := newInstance(cfg.ReadOnly, ic)
		if err != nil {
			return nil, err
		}
		r.instances[ic.Name] = in
		r.order = append(r.order, ic.Name)
	}
	if _, ok := r.instances[r.defaultName]; !ok {
		return nil, fmt.Errorf("defaultInstance %q not found after build", r.defaultName)
	}
	return r, nil
}

// NewRegistryForTest assembles a Registry from pre-built instances.
func NewRegistryForTest(defaultName string, ins ...*Instance) *Registry {
	r := &Registry{instances: make(map[string]*Instance, len(ins)), defaultName: defaultName}
	for _, in := range ins {
		r.instances[in.Name] = in
		r.order = append(r.order, in.Name)
	}
	if defaultName == "" && len(ins) > 0 {
		r.defaultName = ins[0].Name
	}
	return r
}

// Get returns the named instance, or the default instance when name is empty. It
// returns a descriptive error listing the valid names on a miss, so the model
// gets an actionable message.
func (r *Registry) Get(name string) (*Instance, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if name == "" {
		name = r.defaultName
	}
	in, ok := r.instances[name]
	if !ok {
		return nil, fmt.Errorf("unknown GitLab instance %q; configured instances: %s", name, strings.Join(r.namesLocked(), ", "))
	}
	return in, nil
}

// Default returns the default instance.
func (r *Registry) Default() *Instance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.instances[r.defaultName]
}

// DefaultName returns the configured default instance name.
func (r *Registry) DefaultName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultName
}

// Names returns the instance names in configuration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.namesLocked()
}

func (r *Registry) namesLocked() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// All returns every instance sorted by name, so instances_list is stable.
func (r *Registry) All() []*Instance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := r.namesLocked()
	sort.Strings(names)
	out := make([]*Instance, 0, len(names))
	for _, n := range names {
		out = append(out, r.instances[n])
	}
	return out
}
