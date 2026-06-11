package bundle

// OCI layer media type constants for configbundle artifacts.
// Import this package everywhere — do not hardcode these strings.
const (
	// MediaTypeManifest is the ConfigBundle manifest layer produced by the bundler enricher.
	MediaTypeManifest = "application/vnd.armada.configbundle.manifest.v1+yaml"

	// MediaTypeData is the DGraph subgraph data layer (data.json.gz) produced by Orbital.
	MediaTypeData = "application/vnd.orbital.subgraph.data.v1+gzip"

	// MediaTypeSchema is the DGraph schema layer (schema.gz) produced by Orbital.
	MediaTypeSchema = "application/vnd.orbital.subgraph.schema.v1+gzip"

	// MediaTypeMapping is the path→orbId mapping layer produced by the bundler
	// alongside the manifest. Orb stores it by bundle digest and uses it to
	// translate K8s field paths to orbId+field at divergence intake time.
	MediaTypeMapping = "application/vnd.armada.configbundle.mapping.v1+json"
)
