package routing

import (
	"sort"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// defaultThreshold is used when the config threshold is zero or negative.
// At 0.35 a message needs at least one strong signal (code block, long text,
// or an attachment) before the heavy model is chosen.
const defaultThreshold = 0.35

// RouterConfig holds the validated model routing settings.
// It mirrors config.RoutingConfig but lives in pkg/routing to keep the
// dependency graph simple: pkg/agent resolves config → routing, not the reverse.
type RouterConfig struct {
	// LightModel is the model_name (from model_list) used for simple tasks.
	LightModel string

	// Threshold is the complexity score cutoff in [0, 1].
	// score >= Threshold → primary (heavy) model.
	// score <  Threshold → light model.
	Threshold float64

	// Tiers is an optional ladder of models ordered light → heavy. A turn goes to
	// the first tier whose MaxScore is greater than its score; above the last tier
	// the primary model is used. When empty, LightModel+Threshold form one tier.
	Tiers []Tier
}

// Tier is one rung of the model ladder.
type Tier struct {
	Model    string
	MaxScore float64 // exclusive upper bound: score < MaxScore selects this tier
}

// Router selects the appropriate model tier for each incoming message.
// It is safe for concurrent use from multiple goroutines.
type Router struct {
	cfg        RouterConfig
	classifier Classifier
}

// New creates a Router with the given config and the default RuleClassifier.
// If cfg.Threshold is zero or negative, defaultThreshold (0.35) is used.
func New(cfg RouterConfig) *Router {
	return newWithClassifier(cfg, &RuleClassifier{})
}

// newWithClassifier creates a Router with a custom Classifier.
// Intended for unit tests that need to inject a deterministic scorer.
func newWithClassifier(cfg RouterConfig, c Classifier) *Router {
	if cfg.Threshold <= 0 {
		cfg.Threshold = defaultThreshold
	}
	if len(cfg.Tiers) == 0 {
		cfg.Tiers = []Tier{{Model: cfg.LightModel, MaxScore: cfg.Threshold}}
	} else {
		cfg.Tiers = append([]Tier(nil), cfg.Tiers...)
		sort.SliceStable(cfg.Tiers, func(i, j int) bool { return cfg.Tiers[i].MaxScore < cfg.Tiers[j].MaxScore })
		cfg.LightModel = cfg.Tiers[0].Model
	}
	return &Router{cfg: cfg, classifier: c}
}

// SelectModel returns the model to use for this conversation turn along with
// the computed complexity score (for logging and debugging).
//
//   - If score < cfg.Threshold: returns (cfg.LightModel, true, score)
//   - Otherwise:               returns (primaryModel, false, score)
//
// The caller is responsible for resolving the returned model name into
// provider candidates (see AgentInstance.LightCandidates).
func (r *Router) SelectModel(
	msg string,
	history []providers.Message,
	primaryModel string,
) (model string, usedLight bool, score float64) {
	features := ExtractFeatures(msg, history)
	score = r.classifier.Score(features)
	for _, t := range r.cfg.Tiers {
		if score < t.MaxScore {
			return t.Model, true, score
		}
	}
	return primaryModel, false, score
}

// Tiers returns the ladder, lightest first.
func (r *Router) Tiers() []Tier {
	return r.cfg.Tiers
}

// LightModel returns the configured light model name.
func (r *Router) LightModel() string {
	return r.cfg.LightModel
}

// Threshold returns the complexity threshold in use.
func (r *Router) Threshold() float64 {
	return r.cfg.Threshold
}
