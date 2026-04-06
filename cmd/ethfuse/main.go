package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/iwanbk/ethfuse/internal/cache"
	"github.com/iwanbk/ethfuse/internal/config"
	ethfs "github.com/iwanbk/ethfuse/internal/fs"
	"github.com/iwanbk/ethfuse/internal/rpc"
)

func main() {
	rpcURL := flag.String("rpc-url", "", "Ethereum RPC endpoint")
	flag.Parse()

	mountpoint := flag.Arg(0)
	if mountpoint == "" {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <mountpoint>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Load configuration with priority: CLI flag > env var > config file
	cfg, err := config.Load(*rpcURL)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if cfg.RPCURL == "" {
		log.Fatal("RPC URL is required. Use -rpc-url flag, ETHFUSE_RPC_URL env var, or config file.")
	}

	// Create RPC client
	client, err := rpc.New(cfg.RPCURL)
	if err != nil {
		log.Fatalf("Failed to create RPC client: %v", err)
	}
	defer client.Close()

	// Create cache
	// TODO: configure cache DB path
	cache, err := cache.NewCache("")
	if err != nil {
		log.Fatalf("Failed to create cache: %v", err)
	}
	defer func() {
		if closeErr := cache.Close(); closeErr != nil {
			log.Printf("Warning: failed to close cache: %v", closeErr)
		}
	}()

	// Create root node
	root := ethfs.NewRootNode(client, cache)

	// Mount the filesystem
	server, err := mount(mountpoint, root)
	if err != nil {
		log.Fatalf("Failed to mount filesystem: %v", err)
	}

	// Handle graceful shutdown on SIGINT/SIGTERM
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received shutdown signal, unmounting...")
		if err := server.Unmount(); err != nil {
			log.Printf("Warning: failed to unmount: %v", err)
		}
	}()

	// Wait for the server to exit
	server.Wait()
}

// mount creates and mounts the FUSE filesystem.
func mount(mountpoint string, root fs.InodeEmbedder) (*fuse.Server, error) {
	opts := &fs.Options{
		MountOptions: fuse.MountOptions{
			FsName: "ethfuse",
			Name:   "ethfuse",
		},
	}

	server, err := fs.Mount(mountpoint, root, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to mount: %w", err)
	}

	log.Printf("Mounted ethfuse at %s", mountpoint)
	return server, nil
}
