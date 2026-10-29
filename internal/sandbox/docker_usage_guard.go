package sandbox

import "sync"

// The reaper and users of a container share a process-local guard. A sweep
// never waits behind live work; an exec, terminal, connect or snapshot keeps
// its read lease until it is finished. Docker ExecInspect additionally guards
// work started by other clients.
var dockerUsageGuards = struct {
	sync.Mutex
	items map[string]*dockerUsageGuard
}{items: make(map[string]*dockerUsageGuard)}

type dockerUsageGuard struct {
	mu   sync.RWMutex
	refs int
}

func (c *DockerRemoteClient) usageGuard(id string) (*dockerUsageGuard, func()) {
	key := c.settings.Endpoint.key() + "\x00" + id
	dockerUsageGuards.Lock()
	g := dockerUsageGuards.items[key]
	if g == nil {
		g = &dockerUsageGuard{}
		dockerUsageGuards.items[key] = g
	}
	g.refs++
	dockerUsageGuards.Unlock()
	return g, func() {
		dockerUsageGuards.Lock()
		g.refs--
		if g.refs == 0 {
			delete(dockerUsageGuards.items, key)
		}
		dockerUsageGuards.Unlock()
	}
}

func (c *DockerRemoteClient) useContainer(id string) func() {
	g, releaseRef := c.usageGuard(id)
	g.mu.RLock()
	return func() { g.mu.RUnlock(); releaseRef() }
}

func (c *DockerRemoteClient) tryReapContainer(id string) (func(), bool) {
	g, releaseRef := c.usageGuard(id)
	if !g.mu.TryLock() {
		releaseRef()
		return nil, false
	}
	return func() { g.mu.Unlock(); releaseRef() }, true
}
