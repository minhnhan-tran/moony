package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Dictionary holds predefined translations for the demo.
var Dictionary = map[string]map[string]string{
	"hello": {
		"es": "hola",
		"fr": "bonjour",
		"de": "hallo",
	},
	"learning": {
		"es": "aprendizaje",
		"fr": "apprentissage",
		"de": "lernen",
	},
	"world": {
		"es": "mundo",
		"fr": "monde",
		"de": "welt",
	},
	"friend": {
		"es": "amigo",
		"fr": "ami",
		"de": "freund",
	},
	"thank you": {
		"es": "gracias",
		"fr": "merci",
		"de": "danke",
	},
}

// NewDictionaryWorkflow creates and returns the workflow configuration
// demonstrating the Parallel/Join (fan-out/barrier) execution graph.
func NewDictionaryWorkflow() *Workflow {
	return &Workflow{
		ID:          "dictionary_workflow",
		Name:        "Parallel Multilingual Dictionary Lookup",
		Description: "Queries Spanish, French, and German translation APIs concurrently and aggregates results.",
		Nodes: []Node{
			{ID: "start", Type: NodeStart, Data: NodeData{Label: "Start"}},
			{ID: "fork", Type: NodeParallel, Data: NodeData{Label: "Parallel API Fan-Out"}},
			{ID: "translate_es", Type: NodeAgent, Data: NodeData{Label: "Spanish Dictionary Service"}},
			{ID: "translate_fr", Type: NodeAgent, Data: NodeData{Label: "French Dictionary Service"}},
			{ID: "translate_de", Type: NodeAgent, Data: NodeData{Label: "German Dictionary Service"}},
			{ID: "join", Type: NodeJoin, Data: NodeData{Label: "Barrier Sync"}},
			{ID: "aggregator", Type: NodeAgent, Data: NodeData{Label: "Response Aggregator"}},
			{ID: "end", Type: NodeEnd, Data: NodeData{Label: "Done"}},
		},
		Edges: []Edge{
			{ID: "e1", Source: "start", Target: "fork"},
			{ID: "e2", Source: "fork", Target: "translate_es"},
			{ID: "e3", Source: "fork", Target: "translate_fr"},
			{ID: "e4", Source: "fork", Target: "translate_de"},
			{ID: "e5", Source: "translate_es", Target: "join"},
			{ID: "e6", Source: "translate_fr", Target: "join"},
			{ID: "e7", Source: "translate_de", Target: "join"},
			{ID: "e8", Source: "join", Target: "aggregator"},
			{ID: "e9", Source: "aggregator", Target: "end"},
		},
	}
}

// GetDictionaryNodeFunc returns a NodeFunc that implements the execution
// logic for the dictionary workflow, with configurable simulated delay multiplier.
func GetDictionaryNodeFunc(delayMultiplier time.Duration) NodeFunc {
	return func(ctx context.Context, in NodeInput, onDelta func(string)) (NodeOutput, error) {
		// Determine target word to translate.
		word := in.Input
		if startWord, ok := in.Vars["start"].(string); ok && startWord != "" {
			word = startWord
		} else if word == "" {
			if lastOut, ok := in.Vars["lastOutput"]; ok {
				if s, ok := lastOut.(string); ok {
					word = s
				}
			}
		}
		if word == "" {
			word = "hello" // Default word
		}

		wordLower := strings.ToLower(strings.TrimSpace(word))

		switch in.Node.ID {
		case "start":
			if onDelta != nil {
				onDelta("Initializing dictionary workflow for word: '" + word + "'...\n")
			}
			return NodeOutput{Text: word}, nil

		case "translate_es":
			if onDelta != nil {
				onDelta("Contacting Spanish Translation API...\n")
			}
			select {
			case <-time.After(150 * delayMultiplier):
			case <-ctx.Done():
				return NodeOutput{}, ctx.Err()
			}
			translation := ""
			if translations, ok := Dictionary[wordLower]; ok {
				translation = translations["es"]
			} else {
				translation = word + "-es"
			}
			if onDelta != nil {
				onDelta("Spanish Translation resolved: '" + translation + "'\n")
			}
			return NodeOutput{Text: translation, Model: "Mock Spanish API v1.0"}, nil

		case "translate_fr":
			if onDelta != nil {
				onDelta("Contacting French Translation API...\n")
			}
			select {
			case <-time.After(250 * delayMultiplier):
			case <-ctx.Done():
				return NodeOutput{}, ctx.Err()
			}
			translation := ""
			if translations, ok := Dictionary[wordLower]; ok {
				translation = translations["fr"]
			} else {
				translation = word + "-fr"
			}
			if onDelta != nil {
				onDelta("French Translation resolved: '" + translation + "'\n")
			}
			return NodeOutput{Text: translation, Model: "Mock French API v1.2"}, nil

		case "translate_de":
			if onDelta != nil {
				onDelta("Contacting German Translation API...\n")
			}
			select {
			case <-time.After(100 * delayMultiplier):
			case <-ctx.Done():
				return NodeOutput{}, ctx.Err()
			}
			translation := ""
			if translations, ok := Dictionary[wordLower]; ok {
				translation = translations["de"]
			} else {
				translation = word + "-de"
			}
			if onDelta != nil {
				onDelta("German Translation resolved: '" + translation + "'\n")
			}
			return NodeOutput{Text: translation, Model: "Mock German API v2.0"}, nil

		case "join":
			if onDelta != nil {
				onDelta("Barrier Sync reached: merging individual translation branch buses.\n")
			}
			return NodeOutput{Text: "merged"}, nil

		case "aggregator":
			if onDelta != nil {
				onDelta("Aggregating parallel translation streams...\n")
			}
			esVal, _ := in.Vars["translate_es"].(string)
			frVal, _ := in.Vars["translate_fr"].(string)
			deVal, _ := in.Vars["translate_de"].(string)

			result := fmt.Sprintf(
				"=== MULTILINGUAL TRANSLATION REPORT ===\n"+
					"English Word: %s\n"+
					"- Spanish (es): %s\n"+
					"- French (fr):  %s\n"+
					"- German (de):  %s\n"+
					"=======================================",
				word, esVal, frVal, deVal,
			)
			if onDelta != nil {
				onDelta("Aggregation complete. Summary generated.\n")
			}
			return NodeOutput{Text: result, Model: "Aggregator Service v1.0"}, nil

		case "end":
			return NodeOutput{Text: in.Input}, nil

		default:
			return NodeOutput{Text: in.Input}, nil
		}
	}
}
