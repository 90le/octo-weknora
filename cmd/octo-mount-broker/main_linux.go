//go:build linux

// Package main runs the restricted read-only folder mount broker.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Tencent/WeKnora/internal/mountbroker"
	"golang.org/x/sys/unix"
)

func main() {
	configPath := flag.String("roots", "/etc/octo-mount-broker/roots.json", "root-owned allowed-root configuration")
	base := flag.String("mount-base", "/srv/octo-managed-mounts", "dedicated shared mount directory")
	state := flag.String("state", "/var/lib/octo-mount-broker/mounts.json", "root-owned persistent state")
	socket := flag.String("socket", "/run/octo-mount-broker/broker.sock", "private local socket")
	socketGID := flag.Int("socket-gid", 26379, "dedicated application socket group")
	flag.Parse()
	if os.Geteuid() != 0 {
		log.Fatal("mount broker requires root")
	}
	if *socketGID <= 0 {
		log.Fatal("socket group must not be root")
	}
	configInfo, err := os.Lstat(*configPath)
	if err != nil || !configInfo.Mode().IsRegular() || configInfo.Mode().Perm()&0o077 != 0 {
		log.Fatal("allowed-root configuration is unsafe")
	}
	configStat, ok := configInfo.Sys().(*syscall.Stat_t)
	if !ok || configStat.Uid != 0 {
		log.Fatal("allowed-root configuration must be root owned")
	}
	b, err := os.ReadFile(*configPath)
	if err != nil || len(b) > 8192 {
		log.Fatal("allowed-root configuration is unavailable")
	}
	var roots map[string]string
	if json.Unmarshal(b, &roots) != nil {
		log.Fatal("allowed-root configuration is invalid")
	}
	for _, p := range []string{*base, filepath.Dir(*state), filepath.Dir(*socket)} {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
			log.Fatal("broker directory is unavailable or unsafe")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			log.Fatal("broker directory must be root owned")
		}
	}
	if err = mountbroker.ValidateSharedBase(*base); err != nil {
		log.Fatal(err)
	}
	lockFD, err := unix.Open(
		filepath.Join(filepath.Dir(*state), "broker.lock"),
		unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		log.Fatal("cannot open broker lock")
	}
	defer func() { _ = unix.Close(lockFD) }()
	if err = unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		log.Fatal("another mount broker is active")
	}
	server, err := mountbroker.New(roots, *base, *state)
	if err != nil {
		log.Fatal(err)
	}
	server.Restore()
	// Remove only a stale, root-owned Unix socket in the private socket directory.
	// The exclusive lock prevents two service starts racing over that path.
	if info, statErr := os.Lstat(*socket); statErr == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode()&os.ModeSocket == 0 {
			log.Fatal("broker socket path is unsafe")
		}
		if active, dialErr := net.DialTimeout("unix", *socket, 250*time.Millisecond); dialErr == nil {
			_ = active.Close()
			log.Fatal("broker socket is already active")
		}
		if err = os.Remove(*socket); err != nil {
			log.Fatal("cannot remove stale broker socket")
		}
	} else if !os.IsNotExist(statErr) {
		log.Fatal("cannot inspect broker socket")
	}
	listener, err := net.Listen("unix", *socket)
	if err != nil {
		log.Fatal("cannot listen on broker socket: ", err)
	}
	defer func() { _ = listener.Close() }()
	if err = os.Chown(*socket, 0, *socketGID); err != nil {
		log.Fatal(err)
	}
	if err = os.Chmod(*socket, 0o660); err != nil {
		log.Fatal(err)
	}
	log.Print("read-only mount broker ready")
	httpServer := &http.Server{
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	if err = httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
