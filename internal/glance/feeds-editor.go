package glance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// The feeds editor lets users manage the RSS tabs of a group widget from the browser.
// It edits the config file on disk directly (preserving comments and any options it
// doesn't know about) and relies on the config file watcher to reload the app.

var feedsEditorTemplate = mustParseTemplate("feeds-editor.html", "document.html", "footer.html")

const (
	feedsEditorMaxBodySize        = 256 * 1024
	feedsEditorMaxSections        = 50
	feedsEditorMaxFeedsPerSection = 200
	feedsEditorMaxTitleLength     = 100
	feedsEditorMaxURLLength       = 2048
)

var feedsEditorMu sync.Mutex

type feedsEditorFeed struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

type feedsEditorSection struct {
	// Position of the section in the config when it was loaded, -1 for new sections.
	// Used to keep the options of existing sections (limit, style, etc) when saving.
	Index int               `json:"index"`
	Title string            `json:"title"`
	Feeds []feedsEditorFeed `json:"feeds"`
}

type feedsEditorData struct {
	Sections []feedsEditorSection `json:"sections"`
}

func (a *application) handleFeedsEditorPageRequest(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, redirectToLogin) {
		return
	}

	data := templateData{App: a}
	a.populateTemplateRequestData(&data.Request, r)

	var responseBytes bytes.Buffer
	if err := feedsEditorTemplate.Execute(&responseBytes, data); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	w.Write(responseBytes.Bytes())
}

func (a *application) handleGetFeedSectionsRequest(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}

	feedsEditorMu.Lock()
	defer feedsEditorMu.Unlock()

	_, sectionsNode, err := a.loadFeedSectionsNode()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}

	data := feedsEditorData{Sections: make([]feedsEditorSection, 0, len(sectionsNode.Content))}

	for i, widgetNode := range sectionsNode.Content {
		section := feedsEditorSection{
			Index: i,
			Title: yamlMappingScalar(widgetNode, "title"),
			Feeds: []feedsEditorFeed{},
		}

		if feedsNode := yamlMappingValue(widgetNode, "feeds"); feedsNode != nil {
			for _, feedNode := range feedsNode.Content {
				section.Feeds = append(section.Feeds, feedsEditorFeed{
					URL:   yamlMappingScalar(feedNode, "url"),
					Title: yamlMappingScalar(feedNode, "title"),
				})
			}
		}

		data.Sections = append(data.Sections, section)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (a *application) handleSaveFeedSectionsRequest(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}

	var data feedsEditorData
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, feedsEditorMaxBodySize)).Decode(&data); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %v", err))
		return
	}

	if err := data.normalizeAndValidate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	feedsEditorMu.Lock()
	defer feedsEditorMu.Unlock()

	document, sectionsNode, err := a.loadFeedSectionsNode()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}

	if err := applyFeedSections(sectionsNode, data.Sections); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}

	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Errorf("encoding config: %v", err))
		return
	}
	encoder.Close()

	// Includes get resolved relative to the config file, only validate when there are
	// none since otherwise we'd be validating an incomplete config
	if !configIncludePattern.Match(encoded.Bytes()) {
		if _, err := newConfigFromYAML(encoded.Bytes()); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Errorf("resulting config is invalid: %v", err))
			return
		}
	}

	if err := writeFileAtomically(a.configPath, encoded.Bytes()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Errorf("writing config: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok": true}`))
}

func (d *feedsEditorData) normalizeAndValidate() error {
	if len(d.Sections) == 0 {
		return errors.New("at least one tab is required")
	}

	if len(d.Sections) > feedsEditorMaxSections {
		return fmt.Errorf("at most %d tabs are allowed", feedsEditorMaxSections)
	}

	seenIndexes := make(map[int]struct{})

	for i := range d.Sections {
		section := &d.Sections[i]
		section.Title = strings.TrimSpace(section.Title)

		if section.Title == "" {
			return fmt.Errorf("tab #%d is missing a title", i+1)
		}

		if len(section.Title) > feedsEditorMaxTitleLength {
			return fmt.Errorf("title of tab %q is too long", section.Title)
		}

		if section.Index >= 0 {
			if _, exists := seenIndexes[section.Index]; exists {
				return fmt.Errorf("tab %q references an existing tab more than once", section.Title)
			}
			seenIndexes[section.Index] = struct{}{}
		}

		if len(section.Feeds) == 0 {
			return fmt.Errorf("tab %q needs at least one feed", section.Title)
		}

		if len(section.Feeds) > feedsEditorMaxFeedsPerSection {
			return fmt.Errorf("tab %q has more than %d feeds", section.Title, feedsEditorMaxFeedsPerSection)
		}

		for f := range section.Feeds {
			feed := &section.Feeds[f]
			feed.URL = strings.TrimSpace(feed.URL)
			feed.Title = strings.TrimSpace(feed.Title)

			if len(feed.URL) > feedsEditorMaxURLLength {
				return fmt.Errorf("feed #%d in tab %q has a URL that is too long", f+1, section.Title)
			}

			parsed, err := url.Parse(feed.URL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return fmt.Errorf("feed #%d in tab %q has an invalid URL, it must start with http:// or https://", f+1, section.Title)
			}

			if len(feed.Title) > feedsEditorMaxTitleLength {
				return fmt.Errorf("feed #%d in tab %q has a title that is too long", f+1, section.Title)
			}
		}
	}

	return nil
}

// Returns the parsed config document along with the "widgets" sequence node of the
// first group widget that only contains rss widgets, which is what the editor manages
func (a *application) loadFeedSectionsNode() (*yaml.Node, *yaml.Node, error) {
	if a.configPath == "" {
		return nil, nil, errors.New("config file path is unknown")
	}

	contents, err := os.ReadFile(a.configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading config: %v", err)
	}

	document := &yaml.Node{}
	if err := yaml.Unmarshal(contents, document); err != nil {
		return nil, nil, fmt.Errorf("parsing config: %v", err)
	}

	sectionsNode := findRSSGroupWidgetsNode(document)
	if sectionsNode == nil {
		return nil, nil, fmt.Errorf(
			"could not find a group widget containing only rss widgets in %s (groups defined in included files are not supported)",
			a.configPath,
		)
	}

	return document, sectionsNode, nil
}

func findRSSGroupWidgetsNode(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.MappingNode && yamlMappingScalar(node, "type") == "group" {
		widgetsNode := yamlMappingValue(node, "widgets")

		if widgetsNode != nil && widgetsNode.Kind == yaml.SequenceNode && len(widgetsNode.Content) > 0 {
			allRSS := true
			for _, child := range widgetsNode.Content {
				if yamlMappingScalar(child, "type") != "rss" {
					allRSS = false
					break
				}
			}

			if allRSS {
				return widgetsNode
			}
		}
	}

	for _, child := range node.Content {
		if found := findRSSGroupWidgetsNode(child); found != nil {
			return found
		}
	}

	return nil
}

func applyFeedSections(sectionsNode *yaml.Node, sections []feedsEditorSection) error {
	existing := sectionsNode.Content
	updated := make([]*yaml.Node, 0, len(sections))

	for _, section := range sections {
		var widgetNode *yaml.Node

		if section.Index >= 0 {
			if section.Index >= len(existing) {
				return fmt.Errorf("tab %q no longer exists in the config, reload the page and try again", section.Title)
			}
			widgetNode = existing[section.Index]
		} else {
			widgetNode = &yaml.Node{Kind: yaml.MappingNode}
			yamlSetMappingScalar(widgetNode, "type", "rss")
		}

		yamlSetMappingScalar(widgetNode, "title", section.Title)

		feedsNode := yamlMappingValue(widgetNode, "feeds")
		if feedsNode == nil || feedsNode.Kind != yaml.SequenceNode {
			feedsNode = &yaml.Node{Kind: yaml.SequenceNode}
			yamlSetMappingValue(widgetNode, "feeds", feedsNode)
		}

		// reuse existing feed nodes so that options like limit or headers are kept
		existingFeeds := make(map[string][]*yaml.Node)
		for _, feedNode := range feedsNode.Content {
			feedURL := yamlMappingScalar(feedNode, "url")
			existingFeeds[feedURL] = append(existingFeeds[feedURL], feedNode)
		}

		updatedFeeds := make([]*yaml.Node, 0, len(section.Feeds))
		for _, feed := range section.Feeds {
			var feedNode *yaml.Node

			if candidates := existingFeeds[feed.URL]; len(candidates) > 0 {
				feedNode = candidates[0]
				existingFeeds[feed.URL] = candidates[1:]
			} else {
				feedNode = &yaml.Node{Kind: yaml.MappingNode}
				yamlSetMappingScalar(feedNode, "url", feed.URL)
			}

			if feed.Title != "" {
				yamlSetMappingScalar(feedNode, "title", feed.Title)
			} else {
				yamlDeleteMappingKey(feedNode, "title")
			}

			updatedFeeds = append(updatedFeeds, feedNode)
		}

		feedsNode.Content = updatedFeeds
		updated = append(updated, widgetNode)
	}

	sectionsNode.Content = updated

	return nil
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}

	return nil
}

func yamlMappingScalar(node *yaml.Node, key string) string {
	value := yamlMappingValue(node, key)
	if value == nil || value.Kind != yaml.ScalarNode {
		return ""
	}

	return value.Value
}

func yamlSetMappingValue(node *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = value
			return
		}
	}

	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func yamlSetMappingScalar(node *yaml.Node, key, value string) {
	if existing := yamlMappingValue(node, key); existing != nil && existing.Kind == yaml.ScalarNode {
		existing.Value = value
		existing.Tag = "!!str"
		existing.Style = 0
		return
	}

	yamlSetMappingValue(node, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func yamlDeleteMappingKey(node *yaml.Node, key string) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

// Writes to a temporary file and renames it over the destination so that the config
// watcher never sees a partially written file. Falls back to writing in place when
// renaming isn't possible, such as when the file itself is bind mounted into a container.
func writeFileAtomically(path string, contents []byte) error {
	mode := os.FileMode(0o644)
	if stat, err := os.Stat(path); err == nil {
		mode = stat.Mode().Perm()
	}

	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err == nil {
		tempPath := temp.Name()
		_, writeErr := temp.Write(contents)
		closeErr := temp.Close()

		if writeErr == nil && closeErr == nil {
			os.Chmod(tempPath, mode)
			if err = os.Rename(tempPath, path); err == nil {
				return nil
			}
		}

		os.Remove(tempPath)
	}

	return os.WriteFile(path, contents, mode)
}

func writeJSONError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
