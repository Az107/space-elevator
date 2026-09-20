package commands

import (
	"fmt"

	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/podman"
	"github.com/spf13/cobra"
)

var imagesCmd = &cobra.Command{
	Use:   "images",
	Short: "List local images",
	RunE:  runImages,
}

func runImages(cmd *cobra.Command, args []string) error {
	cfg := config.Default()
	client, err := podman.New(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer client.Close()

	imgs, err := client.ListImages(cmd.Context())
	if err != nil {
		return err
	}

	if len(imgs) == 0 {
		fmt.Println("No images found.")
		return nil
	}

	fmt.Printf("%-14s %-50s %-12s %s\n", "ID", "REPOSITORY:TAG", "SIZE", "CREATED")
	for _, im := range imgs {
		tag := "<none>:<none>"
		if len(im.RepoTags) > 0 && im.RepoTags[0] != "<none>:<none>" {
			tag = im.RepoTags[0]
		}
		short := tag
		if len(short) > 50 {
			short = short[:47] + "..."
		}
		fmt.Printf("%-14s %-50s %-12s %s\n", im.ID, short, formatBytes(im.Size), im.Created.Format("2006-01-02 15:04"))
	}
	return nil
}

func formatBytes(b int64) string {
	const k = 1024
	switch {
	case b >= k*k*k:
		return fmt.Sprintf("%.1fGB", float64(b)/(k*k*k))
	case b >= k*k:
		return fmt.Sprintf("%.1fMB", float64(b)/(k*k))
	case b >= k:
		return fmt.Sprintf("%.1fKB", float64(b)/k)
	default:
		return fmt.Sprintf("%dB", b)
	}
}
