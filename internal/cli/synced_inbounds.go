package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"proxyctl/internal/renderer"
)

const syncedInboundsFileName = "synced-inbounds.json"

type syncedInboundEntry struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	Engine         string `json:"engine"`
	NodeID         string `json:"node_id"`
	Domain         string `json:"domain"`
	Port           int    `json:"port"`
	TLSEnabled     bool   `json:"tls_enabled"`
	RealityEnabled bool   `json:"reality_enabled"`
	Transport      string `json:"transport"`
	Path           string `json:"path"`
	SNI            string `json:"sni"`
	VLESSFlow      string `json:"vless_flow"`
	Enabled        bool   `json:"enabled"`
	CreatedAt      string `json:"created_at"`
}

type syncedInboundsSnapshot struct {
	Source    string               `json:"source"`
	NodeID    string               `json:"node_id"`
	Generated string               `json:"generated_at"`
	Inbounds  []syncedInboundEntry `json:"inbounds"`
}

func buildSyncedInboundsSnapshot(req renderer.BuildRequest) ([]byte, error) {
	inbounds := make([]syncedInboundEntry, 0, len(req.Inbounds))
	for _, inbound := range req.Inbounds {
		inbounds = append(inbounds, syncedInboundEntry{
			ID:             inbound.ID,
			Type:           string(inbound.Type),
			Engine:         string(inbound.Engine),
			NodeID:         inbound.NodeID,
			Domain:         inbound.Domain,
			Port:           inbound.Port,
			TLSEnabled:     inbound.TLSEnabled,
			RealityEnabled: inbound.RealityEnabled,
			Transport:      inbound.Transport,
			Path:           inbound.Path,
			SNI:            inbound.SNI,
			VLESSFlow:      inbound.VLESSFlow,
			Enabled:        inbound.Enabled,
			CreatedAt:      inbound.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(inbounds, func(i, j int) bool {
		return inbounds[i].ID < inbounds[j].ID
	})

	payload := syncedInboundsSnapshot{
		Source:    "panel-sync",
		NodeID:    req.Node.ID,
		Generated: time.Now().UTC().Format(time.RFC3339),
		Inbounds:  inbounds,
	}
	return json.MarshalIndent(payload, "", "  ")
}

func readSyncedInboundsSnapshot(runtimeDir string) (syncedInboundsSnapshot, error) {
	path := filepath.Join(strings.TrimSpace(runtimeDir), syncedInboundsFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return syncedInboundsSnapshot{}, err
	}
	var snapshot syncedInboundsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return syncedInboundsSnapshot{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return snapshot, nil
}

func printWizardSyncedInbounds(out io.Writer, runtimeDir string) error {
	snapshot, err := readSyncedInboundsSnapshot(runtimeDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(out, "no synced inbounds snapshot found: %s\n", filepath.Join(strings.TrimSpace(runtimeDir), syncedInboundsFileName))
			fmt.Fprintln(out, "run `proxyctl node sync` from control panel to populate it")
			return nil
		}
		return err
	}

	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SOURCE\tID\tTYPE\tENGINE\tNODE_ID\tDOMAIN\tPORT\tTLS\tREALITY\tTRANSPORT\tPATH\tSNI\tFLOW\tENABLED\tCREATED_AT")
	for _, inbound := range snapshot.Inbounds {
		fmt.Fprintf(
			w,
			"panel-sync\t%s\t%s\t%s\t%s\t%s\t%d\t%t\t%t\t%s\t%s\t%s\t%s\t%t\t%s\n",
			inbound.ID,
			inbound.Type,
			inbound.Engine,
			inbound.NodeID,
			inbound.Domain,
			inbound.Port,
			inbound.TLSEnabled,
			inbound.RealityEnabled,
			inbound.Transport,
			inbound.Path,
			inbound.SNI,
			inbound.VLESSFlow,
			inbound.Enabled,
			inbound.CreatedAt,
		)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(out, "snapshot source=%s node_id=%s generated_at=%s\n", snapshot.Source, snapshot.NodeID, snapshot.Generated)
	return nil
}
