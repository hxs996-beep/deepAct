package memory

import (
	"fmt"
	"strings"
	"time"

	"github.com/deepact/deepact/engine"
)

// RenderMarkdown renders a human-readable summary of the snapshot. It is the
// human-facing view of the machine-readable memory.json (which the model
// consumes via Block B).
func RenderMarkdown(snap *engine.MemorySnapshot) string {
	var b strings.Builder
	b.WriteString("# Memory\n")

	updated := "unknown"
	if snap != nil && !snap.UpdatedAt.IsZero() {
		updated = snap.UpdatedAt.Format(time.RFC3339)
	}
	b.WriteString("> 最后更新: " + updated + "\n")
	if snap != nil && snap.CWD != "" {
		b.WriteString("> 项目: " + snap.CWD + "\n")
	}
	b.WriteString("\n")

	if snap == nil {
		b.WriteString("（暂无记忆）\n")
		return b.String()
	}

	writeSection := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		b.WriteString("## " + title + "\n")
		for _, it := range items {
			b.WriteString("- " + it + "\n")
		}
		b.WriteString("\n")
	}

	writeSection("关键发现", snap.MemoryMarkers)

	if len(snap.Decisions) > 0 {
		b.WriteString("## 决策记录\n")
		for _, d := range snap.Decisions {
			label := d.ID
			if label == "" {
				label = "-"
			}
			b.WriteString(fmt.Sprintf("- %s: %s\n", label, d.Text))
		}
		b.WriteString("\n")
	}

	writeSection("待解决问题", snap.OpenQuestions)
	writeSection("假设", snap.Assumptions)

	return b.String()
}
