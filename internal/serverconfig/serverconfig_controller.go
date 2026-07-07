package serverconfig

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	armadav1 "github.com/armada/configbundle/api/v1"
)

// ConditionReconciled is the canonical condition type written by this
// controller. Type=True ⇒ live state matches intent (after PATCH, or already
// matching on read). Type=False ⇒ a step failed; Reason names which.
const ConditionReconciled = "Reconciled"

// iDRAC attribute keys for the toggles this controller manages. Add new
// entries here as more settings are wired in, and extend computeIdracDeltas
// + unmanagedFields + policyBlockedFields + the KnownIdracFields registry
// below.
const (
	attrSSHEnable    = "SSH.1.Enable"
	attrRacadmEnable = "Racadm.1.Enable"
	attrIPMIEnable   = "IPMILan.1.Enable"
)

// KnownIdracFields enumerates every CRD JSON field name this controller
// recognizes for reconciliation. Used at startup to warn about unknown
// entries in IDRAC_FIELD_ALLOWLIST and to gate which intents the controller
// will act on. The names match the JSON tags on armadav1.IdracSettingsSpec.
var KnownIdracFields = []string{"sshEnabled", "racadmEnabled", "ipmiEnabled"}

// UnknownAllowlistEntries returns any entries in `allowed` that don't name
// a field this controller knows how to reconcile. Used by main.go to warn
// about typos at startup.
func UnknownAllowlistEntries(allowed map[string]bool) []string {
	known := map[string]bool{}
	for _, f := range KnownIdracFields {
		known[f] = true
	}
	var unknown []string
	for k := range allowed {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// enabledStr / disabledStr are the canonical Attributes-API values for a
// bool-flavored toggle on Dell iDRAC. iDRAC sometimes capitalizes inconsistently
// across firmware versions, so reads are compared case-insensitively.
const (
	enabledStr  = "Enabled"
	disabledStr = "Disabled"
)

func boolToEnableStr(b bool) string {
	if b {
		return enabledStr
	}
	return disabledStr
}

func enableStrToBool(s string) bool {
	return strings.EqualFold(s, enabledStr)
}

// computeIdracDeltas returns the subset of (attribute → intent-string) pairs
// that need PATCHing — i.e., spec sets an intent, the field is allowed by
// policy, AND live differs from intent. Empty result means "nothing to do."
// Pure function: no K8s, no HTTP — straightforward to unit-test.
func computeIdracDeltas(spec armadav1.IdracSettingsSpec, live map[string]any, allowed map[string]bool) map[string]string {
	out := map[string]string{}
	if spec.SSHEnabled != nil && allowed["sshEnabled"] {
		intentStr := boolToEnableStr(*spec.SSHEnabled)
		if liveStr, _ := live[attrSSHEnable].(string); !strings.EqualFold(liveStr, intentStr) {
			out[attrSSHEnable] = intentStr
		}
	}
	if spec.RacadmEnabled != nil && allowed["racadmEnabled"] {
		intentStr := boolToEnableStr(*spec.RacadmEnabled)
		if liveStr, _ := live[attrRacadmEnable].(string); !strings.EqualFold(liveStr, intentStr) {
			out[attrRacadmEnable] = intentStr
		}
	}
	if spec.IPMIEnabled != nil && allowed["ipmiEnabled"] {
		intentStr := boolToEnableStr(*spec.IPMIEnabled)
		if liveStr, _ := live[attrIPMIEnable].(string); !strings.EqualFold(liveStr, intentStr) {
			out[attrIPMIEnable] = intentStr
		}
	}
	return out
}

// hasReconcilableIntent reports whether spec specifies any setting that is
// BOTH (a) recognized by this controller and (b) permitted by the field
// allowlist. Used to short-circuit before loading creds or touching the
// network. Note: fields with intent set but blocked by policy don't count —
// the early-return log surfaces them via policyBlockedFields().
func hasReconcilableIntent(spec armadav1.IdracSettingsSpec, allowed map[string]bool) bool {
	return (spec.SSHEnabled != nil && allowed["sshEnabled"]) ||
		(spec.RacadmEnabled != nil && allowed["racadmEnabled"]) ||
		(spec.IPMIEnabled != nil && allowed["ipmiEnabled"])
}

// unmanagedFields lists the managed-field names whose intent is nil on the
// given spec. Order is stable (declaration order). Returned in the summary
// log so operators can see which fields the controller deliberately ignored
// (no intent) vs. fields it acted on.
func unmanagedFields(spec armadav1.IdracSettingsSpec) []string {
	var out []string
	if spec.SSHEnabled == nil {
		out = append(out, "sshEnabled")
	}
	if spec.RacadmEnabled == nil {
		out = append(out, "racadmEnabled")
	}
	if spec.IPMIEnabled == nil {
		out = append(out, "ipmiEnabled")
	}
	return out
}

// policyBlockedFields lists fields where intent IS set in the spec but the
// field is NOT in the allow set. Logged so operators can see why a field
// they expected to be reconciled didn't fire.
func policyBlockedFields(spec armadav1.IdracSettingsSpec, allowed map[string]bool) []string {
	var out []string
	if spec.SSHEnabled != nil && !allowed["sshEnabled"] {
		out = append(out, "sshEnabled")
	}
	if spec.RacadmEnabled != nil && !allowed["racadmEnabled"] {
		out = append(out, "racadmEnabled")
	}
	if spec.IPMIEnabled != nil && !allowed["ipmiEnabled"] {
		out = append(out, "ipmiEnabled")
	}
	return out
}

// ServerConfigReconciler watches ServerConfig CRs and reconciles spec.idrac
// against the iDRAC at spec.oobIP via Redfish. Prototype scope: only the
// SSH.ProtocolEnabled setting is wired through.
type ServerConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// AllowedOobIPs is the set of OOB IPs the controller will reconcile against.
	// CRs whose spec.oobIP is not in this set are logged and skipped without any
	// status writes. Empty set = nothing is reconciled (paranoid default).
	AllowedOobIPs map[string]bool

	// CredentialsNamespace + CredentialsSecretName name the Secret carrying
	// `username` + `password` keys used for Redfish basic auth. The same Secret
	// is used for every iDRAC in this prototype.
	CredentialsNamespace  string
	CredentialsSecretName string

	// AllowedFields is the set of CRD JSON field names the controller is
	// permitted to reconcile. Fields with intent set on a CR but not in this
	// set are dropped from the PATCH (logged as `policyBlocked`). Empty set =
	// paranoid (nothing reconciled). Same shape as AllowedOobIPs.
	AllowedFields map[string]bool

	// ObserveInterval is the cadence at which the reconciler re-polls iDRAC
	// even when nothing on the CR has changed. Drives drift detection: without
	// it, an out-of-band iDRAC change (someone toggles via the web UI) would
	// stay invisible until the next spec change. Zero = no periodic poll
	// (event-driven only).
	ObserveInterval time.Duration
}

func (r *ServerConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// GenerationChangedPredicate fires reconcile on Create + spec changes only.
	// Status updates and managedFields-only changes don't bump generation, so
	// they don't re-fire. Combined with our "log once per change" semantic,
	// this keeps the log clean: one log per CR at startup (cache sync), then
	// silence until spec.idrac.* actually changes.
	return ctrl.NewControllerManagedBy(mgr).
		For(&armadav1.ServerConfig{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("serverconfig").
		Complete(r)
}

func (r *ServerConfigReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithName("serverconfig")

	var sc armadav1.ServerConfig
	if err := r.Get(ctx, req.NamespacedName, &sc); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("serverconfig deleted", "name", req.Name, "namespace", req.Namespace)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// No oobIP means nothing actionable — surface the skip on status so
	// `kubectl describe sc <name>` explains the blank live state.
	if sc.Spec.OobIP == nil || *sc.Spec.OobIP == "" {
		logger.Info("no oobIP on serverconfig; skipping",
			"name", sc.Name, "serviceTag", sc.Spec.ServiceTag)
		r.setStatusSkipped(ctx, &sc, "NoOobIP",
			"spec.oobIP is empty — no target to reconcile against. Populate spec.oobIP to enable reconciliation.")
		return reconcile.Result{}, nil
	}
	oobIP := *sc.Spec.OobIP

	// Allowlist enforcement — short-circuit before fetching credentials or
	// touching the network. Status is written so operators can distinguish
	// "controller broken" from "this CR is deliberately not managed by this
	// controller instance" without reading logs.
	if !r.AllowedOobIPs[oobIP] {
		logger.Info("change detected but oobIP not on allowlist, ignoring",
			"name", sc.Name, "oobIP", oobIP,
			"intent.sshEnabled", boolPtr(sc.Spec.IdracSettings.SSHEnabled),
			"intent.racadmEnabled", boolPtr(sc.Spec.IdracSettings.RacadmEnabled),
			"intent.ipmiEnabled", boolPtr(sc.Spec.IdracSettings.IPMIEnabled))
		r.setStatusSkipped(ctx, &sc, "NotInOobAllowlist",
			fmt.Sprintf("oobIP %s is not in IDRAC_OOB_ALLOWLIST — this ServerConfig is not managed by this controller instance.", oobIP))
		return reconcile.Result{}, nil
	}

	// Look up the parent ConfigBundle so we can mirror its spec.ignored[]
	// entries into the ignored gauge. Best-effort: if the parent isn't
	// reachable (transient cache miss, RBAC tightening), we still emit
	// intent + observed gauges — the ignored gauge just stays absent for
	// this server until the next reconcile.
	ignoredFields := ignoredFieldsForServer(ctx, r.Client, &sc, logger)
	recordIgnored(sc.Name, oobIP, ignoredFields, r.AllowedFields)

	// Nothing reconcilable on this CR — either no intent set, or all intent
	// fields are blocked by the field allowlist. Surface both states.
	if !hasReconcilableIntent(sc.Spec.IdracSettings, r.AllowedFields) {
		logger.Info("no reconcilable intent on serverconfig; skipping",
			"name", sc.Name, "oobIP", oobIP,
			"unmanaged", unmanagedFields(sc.Spec.IdracSettings),
			"policyBlocked", policyBlockedFields(sc.Spec.IdracSettings, r.AllowedFields))
		recordIntent(sc.Name, oobIP, sc.Spec.IdracSettings, r.AllowedFields)
		return reconcile.Result{}, nil
	}

	// Refresh the intent gauge before the network step so it stays in sync
	// with the CR even if the Redfish GET fails.
	recordIntent(sc.Name, oobIP, sc.Spec.IdracSettings, r.AllowedFields)

	// Load shared credentials.
	creds, err := loadIdracCredentials(ctx, r.Client, r.CredentialsNamespace, r.CredentialsSecretName)
	if err != nil {
		// Treat "Secret not found" as a config error, not a transient one —
		// controller-runtime's exponential backoff would otherwise spam the
		// log with stack traces 8×/sec until the operator creates the Secret.
		// Long requeue lets us recover automatically without the noise.
		if apierrors.IsNotFound(err) {
			logger.Info("iDRAC credentials Secret not found; deferring reconcile (create the Secret to proceed)",
				"name", sc.Name,
				"secret", r.CredentialsNamespace+"/"+r.CredentialsSecretName)
			r.setStatusFailed(ctx, &sc, "MissingCredentials", err.Error())
			recordReconcileError(sc.Name, "MissingCredentials")
			return reconcile.Result{RequeueAfter: 1 * time.Minute}, nil
		}
		logger.Error(err, "load iDRAC credentials")
		r.setStatusFailed(ctx, &sc, "CredentialsLoadFailed", err.Error())
		recordReconcileError(sc.Name, "CredentialsLoadFailed")
		return reconcile.Result{}, fmt.Errorf("load credentials: %w", err)
	}

	// Read live iDRAC attributes, compute deltas across every managed field.
	rc := newRedfishClient(oobIP, creds.Username, creds.Password)
	attrs, err := rc.GetAttributes(ctx)
	if err != nil {
		logger.Error(err, "read iDRAC Attributes", "oobIP", oobIP)
		r.setStatusFailed(ctx, &sc, "RedfishReadFailed", err.Error())
		recordReconcileError(sc.Name, "RedfishReadFailed")
		return reconcile.Result{}, fmt.Errorf("read Attributes from %s: %w", oobIP, err)
	}

	recordObserved(sc.Name, oobIP, attrs, r.AllowedFields)

	deltas := computeIdracDeltas(sc.Spec.IdracSettings, attrs, r.AllowedFields)
	unmanaged := unmanagedFields(sc.Spec.IdracSettings)
	blocked := policyBlockedFields(sc.Spec.IdracSettings, r.AllowedFields)

	if len(deltas) == 0 {
		logger.Info("reconciled (no PATCH needed)",
			"name", sc.Name, "oobIP", oobIP,
			"unmanaged", unmanaged,
			"policyBlocked", blocked)
		// Don't overwrite a prior "PATCHed N attribute(s)" status message on
		// every periodic poll — that erases the useful action history. Status
		// is for "what the controller last did," not "what we just observed";
		// live values flow to Prometheus gauges instead. Only refresh status
		// here if we're recovering from a non-Reconciled state.
		if !isCurrentlyReconciled(&sc) {
			r.setStatusApplied(ctx, &sc, "all managed settings already match intent")
		} else {
			// Still bump observedGeneration so tooling can detect we've seen
			// the latest spec. Cheap — skipped entirely when already caught up.
			r.bumpObservedGeneration(ctx, &sc)
		}
		// Populate the per-field observed ledger: iDRAC confirmed-matches-intent
		// for every allowlisted managed field. Skipped if already correct.
		r.recordObserved(ctx, &sc, attrs)
		recordReconcileSuccess(sc.Name, time.Now().Unix())
		return reconcile.Result{RequeueAfter: r.ObserveInterval}, nil
	}

	if err := rc.PatchAttributes(ctx, deltas); err != nil {
		logger.Error(err, "PATCH iDRAC attributes", "oobIP", oobIP, "updates", deltas)
		r.setStatusFailed(ctx, &sc, "RedfishPatchFailed", err.Error())
		recordReconcileError(sc.Name, "RedfishPatchFailed")
		return reconcile.Result{}, fmt.Errorf("PATCH attributes on %s: %w", oobIP, err)
	}
	logger.Info("reconciled (PATCH applied)",
		"name", sc.Name, "oobIP", oobIP,
		"unmanaged", unmanaged,
		"policyBlocked", blocked,
		"updates", deltas)
	patchMsg := fmt.Sprintf("PATCHed %d attribute(s): %s", len(deltas), formatDeltas(deltas))
	r.setStatusApplied(ctx, &sc, patchMsg)
	// Overlay the just-PATCHed values into the attrs snapshot so observed
	// reflects post-PATCH state without a second Redfish GET. Delta keys +
	// values are Dell Attribute-API strings — same shape as attrs — so this
	// is a simple map merge. Fields not in deltas retain their pre-PATCH
	// live value.
	for k, v := range deltas {
		attrs[k] = v
	}
	r.recordObserved(ctx, &sc, attrs)
	// Append to the bounded action-history list so operators can see all
	// PATCHes from a multi-step reconcile (the condition message only holds
	// the latest).
	r.appendRecentPatch(ctx, &sc, patchMsg)
	recordReconcileSuccess(sc.Name, time.Now().Unix())
	return reconcile.Result{RequeueAfter: r.ObserveInterval}, nil
}

// ignoredFieldsForServer returns the set of allowlisted-field names that the
// parent ConfigBundle's spec.ignored[] lists for this ServerConfig. Used to
// label the ignored gauge so alert rules can suppress drift alerts on
// admin-overridden fields.
//
// Best-effort: any failure to resolve the parent (no OwnerReference, CR not
// found, RBAC denied) returns an empty set + a debug log line. The reconcile
// itself is not affected — intent and observed gauges still emit. The ignored
// gauge series is simply absent for this server until the next reconcile
// resolves the parent.
func ignoredFieldsForServer(ctx context.Context, c client.Client, sc *armadav1.ServerConfig, logger logr.Logger) map[string]bool {
	out := map[string]bool{}

	var ownerName string
	for _, ref := range sc.OwnerReferences {
		if ref.Kind == "ConfigBundle" {
			ownerName = ref.Name
			break
		}
	}
	if ownerName == "" {
		logger.V(1).Info("serverconfig has no ConfigBundle ownerRef; ignored gauge will be empty", "name", sc.Name)
		return out
	}

	var cb armadav1.ConfigBundle
	if err := c.Get(ctx, types.NamespacedName{Name: ownerName}, &cb); err != nil {
		logger.V(1).Info("could not load parent ConfigBundle; ignored gauge will be empty", "name", sc.Name, "owner", ownerName, "err", err.Error())
		return out
	}

	for _, ig := range cb.Spec.Ignored {
		if ig.ServerOrbID == sc.Spec.OrbID {
			out[ig.Field] = true
		}
	}
	return out
}

// formatDeltas renders a small map as a stable, human-readable string for
// status messages: "SSH.1.Enable=Disabled, Racadm.1.Enable=Enabled".
func formatDeltas(d map[string]string) string {
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, d[k]))
	}
	return strings.Join(parts, ", ")
}

// setStatusApplied writes Phase=Applied + Reconciled=True. Best-effort: status
// conflicts are logged and dropped; the next reconcile will reassert.
func (r *ServerConfigReconciler) setStatusApplied(ctx context.Context, sc *armadav1.ServerConfig, msg string) {
	r.writeStatus(ctx, sc, armadav1.ServerConfigPhaseApplied, metav1.ConditionTrue, "SSHApplied", msg)
}

// setStatusFailed writes Phase=Diverged + Reconciled=False with a Reason that
// names which step failed (MissingCredentials, RedfishReadFailed, RedfishPatchFailed).
func (r *ServerConfigReconciler) setStatusFailed(ctx context.Context, sc *armadav1.ServerConfig, reason, msg string) {
	r.writeStatus(ctx, sc, armadav1.ServerConfigPhaseDiverged, metav1.ConditionFalse, reason, msg)
}

// setStatusSkipped writes Phase=Skipped + Reconciled=False with a Reason
// describing why the controller deliberately did not reconcile this CR
// (NoOobIP, NotInOobAllowlist). Distinct from setStatusFailed — a Skipped
// CR was never attempted, not tried-and-failed. Surfaces the gate that
// blocked reconciliation so `kubectl describe sc <name>` explains blank
// live state without an operator having to check the controller logs.
func (r *ServerConfigReconciler) setStatusSkipped(ctx context.Context, sc *armadav1.ServerConfig, reason, msg string) {
	r.writeStatus(ctx, sc, armadav1.ServerConfigPhaseSkipped, metav1.ConditionFalse, reason, msg)
}

// writeStatus updates the ServerConfig's Phase + Reconciled condition.
// Wrapped in RetryOnConflict so an immediately-prior Get's resourceVersion
// going stale (e.g. just after `kubectl apply` creates the CR and the cache
// hasn't fully synced) doesn't surface a benign optimistic-concurrency 409.
// Refetch + reapply on conflict; bail out on any non-conflict error.
func (r *ServerConfigReconciler) writeStatus(ctx context.Context, sc *armadav1.ServerConfig, phase armadav1.ServerConfigPhase, condStatus metav1.ConditionStatus, reason, msg string) {
	logger := log.FromContext(ctx).WithName("serverconfig.status")
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh armadav1.ServerConfig
		if err := r.Get(ctx, client.ObjectKeyFromObject(sc), &fresh); err != nil {
			return err
		}
		fresh.Status.Phase = phase
		fresh.Status.ObservedGeneration = fresh.Generation
		setCondition(&fresh.Status.Conditions, ConditionReconciled, condStatus, reason, msg)
		return r.Status().Update(ctx, &fresh)
	})
	if err != nil {
		logger.Info("status update failed (will retry next reconcile)", "name", sc.Name, "err", err.Error())
	}
}

// recentPatchesLimit caps how many PATCH-history entries we keep in
// status.recentPatches. Five is enough to see a typical multi-step reconcile
// (takeover, override-and-revert, drift-correction) in full, while keeping
// status size negligible — each entry is ~200 bytes.
const recentPatchesLimit = 5

// appendRecentPatch prepends a new PATCH-action entry to status.recentPatches
// and truncates the list to recentPatchesLimit. Called only on successful
// Redfish PATCH (never on no-op reconciles) — the list is an action log, not
// an observation log. Read-modify-write with RetryOnConflict.
func (r *ServerConfigReconciler) appendRecentPatch(ctx context.Context, sc *armadav1.ServerConfig, message string) {
	entry := armadav1.RecentPatch{
		Time:    metav1.Now(),
		Message: message,
	}
	logger := log.FromContext(ctx).WithName("serverconfig.status")
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh armadav1.ServerConfig
		if err := r.Get(ctx, client.ObjectKeyFromObject(sc), &fresh); err != nil {
			return err
		}
		recent := make([]armadav1.RecentPatch, 0, recentPatchesLimit+1)
		recent = append(recent, entry)
		recent = append(recent, fresh.Status.RecentPatches...)
		if len(recent) > recentPatchesLimit {
			recent = recent[:recentPatchesLimit]
		}
		fresh.Status.RecentPatches = recent
		return r.Status().Update(ctx, &fresh)
	})
	if err != nil {
		logger.Info("recentPatches update failed (next PATCH will retry)", "name", sc.Name, "err", err.Error())
	}
}

// recordObserved writes status.observed.idracSettings from the live Redfish
// Attributes map (passed in by the caller from the top-of-reconcile GET).
// Live-read semantics: observed reflects what actually exists on the iDRAC,
// not the intent we PATCHed. Consequences:
//   - out-of-band change flips a value → observed shows the LIVE value,
//     drift visible in `kubectl get sc -o yaml` on the next reconcile
//   - field missing from Attributes response (transient firmware quirk) →
//     that field is skipped for this pass, no series update
//   - field not on the allowlist → not written, even if live has a value
//     (matches the Prom metrics semantic in package-level recordObserved)
//
// Read-modify-write with RetryOnConflict; skipped entirely when the freshly
// projected observed equals what's already in status, so periodic polls in
// steady state are zero-cost. attrs is threaded through instead of re-fetched
// inside the retry loop: a fresh Redfish GET per conflict retry would burn
// iDRAC round-trips on unrelated status races.
func (r *ServerConfigReconciler) recordObserved(ctx context.Context, sc *armadav1.ServerConfig, attrs map[string]any) {
	desired := buildObservedIdrac(attrs, r.AllowedFields)
	if observedIdracEqual(sc.Status.Observed.IdracSettings, desired) {
		return
	}
	logger := log.FromContext(ctx).WithName("serverconfig.status")
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh armadav1.ServerConfig
		if err := r.Get(ctx, client.ObjectKeyFromObject(sc), &fresh); err != nil {
			return err
		}
		if observedIdracEqual(fresh.Status.Observed.IdracSettings, desired) {
			return nil
		}
		fresh.Status.Observed.IdracSettings = desired
		return r.Status().Update(ctx, &fresh)
	})
	if err != nil {
		logger.Info("observed status update failed (will retry next reconcile)", "name", sc.Name, "err", err.Error())
	}
}

// buildObservedIdrac projects the live Redfish attribute map into the
// observed-idrac status shape, gated by the field allowlist. Fields absent
// from attrs (missing from firmware response) or not on the allowlist stay
// nil in the output — the ledger only reflects values the controller manages
// AND observed live on the target. Mirror of package-level recordObserved
// (metrics.go) — both share the same live-attr-derivation shape.
func buildObservedIdrac(attrs map[string]any, allowed map[string]bool) armadav1.ObservedIdracSettingsStatus {
	var out armadav1.ObservedIdracSettingsStatus
	set := func(field, attrKey string, dst **bool) {
		if !allowed[field] {
			return
		}
		raw, ok := attrs[attrKey].(string)
		if !ok {
			return
		}
		b := enableStrToBool(raw)
		*dst = &b
	}
	set("sshEnabled", attrSSHEnable, &out.SSHEnabled)
	set("ipmiEnabled", attrIPMIEnable, &out.IPMIEnabled)
	set("racadmEnabled", attrRacadmEnable, &out.RacadmEnabled)
	return out
}

func observedIdracEqual(a, b armadav1.ObservedIdracSettingsStatus) bool {
	return boolPtrEqual(a.SSHEnabled, b.SSHEnabled) &&
		boolPtrEqual(a.IPMIEnabled, b.IPMIEnabled) &&
		boolPtrEqual(a.RacadmEnabled, b.RacadmEnabled)
}

func boolPtrEqual(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// bumpObservedGeneration writes status.observedGeneration = metadata.generation
// if they differ. Called on no-op reconciles (where we skip the full writeStatus
// to preserve the prior action message) so tooling can still observe "controller
// has caught up to my spec change." The "only write on change" guard means
// periodic polls in steady state produce zero apiserver writes.
func (r *ServerConfigReconciler) bumpObservedGeneration(ctx context.Context, sc *armadav1.ServerConfig) {
	if sc.Status.ObservedGeneration == sc.Generation {
		return
	}
	logger := log.FromContext(ctx).WithName("serverconfig.status")
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh armadav1.ServerConfig
		if err := r.Get(ctx, client.ObjectKeyFromObject(sc), &fresh); err != nil {
			return err
		}
		if fresh.Status.ObservedGeneration == fresh.Generation {
			return nil
		}
		fresh.Status.ObservedGeneration = fresh.Generation
		return r.Status().Update(ctx, &fresh)
	})
	if err != nil {
		logger.Info("observedGeneration update failed (will retry next reconcile)", "name", sc.Name, "err", err.Error())
	}
}

// isCurrentlyReconciled reports whether the ServerConfig is in a Reconciled=True
// state. Used to gate status writes during no-op periodic polls so we don't
// erase a more informative "PATCHed N attribute(s)" message with the generic
// "all match intent" text on every drift-detection cycle.
func isCurrentlyReconciled(sc *armadav1.ServerConfig) bool {
	for _, c := range sc.Status.Conditions {
		if c.Type == ConditionReconciled {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
}

// setCondition updates or appends a condition by Type. ObservedGeneration is
// set to the CR's current generation so consumers can detect stale status.
func setCondition(conds *[]metav1.Condition, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i := range *conds {
		c := &(*conds)[i]
		if c.Type != condType {
			continue
		}
		if c.Status != status {
			c.LastTransitionTime = now
		}
		c.Status = status
		c.Reason = reason
		c.Message = message
		return
	}
	*conds = append(*conds, metav1.Condition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	})
}

func boolPtr(p *bool) string {
	if p == nil {
		return "<nil>"
	}
	if *p {
		return "true"
	}
	return "false"
}
