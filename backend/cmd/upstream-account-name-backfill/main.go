package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type output struct {
	Mode        string                                    `json:"mode"`
	Changes     []service.UpstreamAccountNameBackfillItem `json:"changes"`
	Skipped     []service.UpstreamAccountNameBackfillItem `json:"skipped"`
	Unchanged   int                                       `json:"unchanged"`
	ChangeCount int                                       `json:"change_count"`
	SkipCount   int                                       `json:"skip_count"`
}

func main() {
	apply := flag.Bool("apply", false, "apply authoritative upstream account names; default is dry-run")
	timeout := flag.Duration("timeout", 30*time.Second, "database operation timeout")
	flag.Parse()

	cfg, err := config.LoadForBootstrap()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	client, _, err := repository.InitEnt(cfg)
	if err != nil {
		log.Fatalf("init database: %v", err)
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			log.Printf("close database: %v", closeErr)
		}
	}()

	repo := repository.NewUpstreamConfigRepository(client)
	svc := service.NewUpstreamConfigService(repo, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var items []service.UpstreamAccountNameBackfillItem
	if *apply {
		items, err = svc.ApplyAccountNameBackfill(ctx)
	} else {
		items, err = svc.PreviewAccountNameBackfill(ctx)
	}
	if err != nil {
		log.Fatalf("upstream account name backfill: %v", err)
	}

	result := output{Mode: "dry-run"}
	if *apply {
		result.Mode = "apply"
	}
	for _, item := range items {
		switch {
		case item.SkipReason != "":
			result.Skipped = append(result.Skipped, item)
		case item.OldName == item.NewName:
			result.Unchanged++
		default:
			result.Changes = append(result.Changes, item)
		}
	}
	result.ChangeCount = len(result.Changes)
	result.SkipCount = len(result.Skipped)

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "encode result: %v\n", err)
		os.Exit(1)
	}
}
