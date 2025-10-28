package processor

import "strings"

// SanitizeText performs simple cleaning of the text: removes certain tags used
// by the original n8n flow (\u3010 and \u3011) and trims whitespace.
func SanitizeText(s string) string {
    s = strings.ReplaceAll(s, "\u3010", "")
    s = strings.ReplaceAll(s, "\u3011", "")
    return strings.TrimSpace(s)
}