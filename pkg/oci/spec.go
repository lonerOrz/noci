package oci

// OCI Image Specification v1.1.0 Media Types & Artifact Types.
const (
	MediaTypeImageManifest = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeImageConfig   = "application/vnd.oci.image.config.v1+json"
	MediaTypeEmptyConfig   = "application/vnd.oci.empty.v1+json"

	// Nix custom layer media types.
	MediaTypeNixLayerGzip = "application/vnd.nix.cache.layer.v1+tar+gzip"
	MediaTypeNixLayerZstd = "application/vnd.nix.cache.layer.v1+tar+zstd"

	// OCI Spec v1.1 artifact type for Nix cache.
	ArtifactTypeNixCache = "application/vnd.nix.cache.v1"

	// OCI Spec v1.1 compliant empty JSON descriptor (2-byte "{}").
	EmptyConfigDigest = "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"
	EmptyConfigSize   = 2
)

// OCIManifest is an OCI Image Specification v1.1.0 image manifest.
type OCIManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// Descriptor is an OCI content descriptor.
type Descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// DefaultEmptyConfigDescriptor returns an OCI Spec v1.1 compliant empty config descriptor.
func DefaultEmptyConfigDescriptor() Descriptor {
	return Descriptor{
		MediaType: MediaTypeEmptyConfig,
		Size:      EmptyConfigSize,
		Digest:    EmptyConfigDigest,
	}
}
