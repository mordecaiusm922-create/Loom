package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sensitiveBasenames are credential files regardless of where they live.
var sensitiveBasenames = map[string]bool{
	".netrc": true, ".pgpass": true, ".git-credentials": true, ".vault-token": true,
	"credentials.tfrc.json": true, "application_default_credentials.json": true,
	"id_rsa": true, "id_ecdsa": true, "id_ed25519": true, "id_dsa": true,
}

var sensitiveExtensions = []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".tfstate", ".tfstate.backup"}

// sensitiveHomePaths are relative to the user's home directory. A path is
// sensitive when it equals one of these or lives under it.
var sensitiveHomePaths = []string{
	".ssh", ".aws/credentials", ".aws/sso/cache", ".aws/cli/cache", ".kube/config",
	".config/gcloud", ".azure", ".docker/config.json", ".terraform.d/credentials.tfrc.json",
	".gnupg", ".password-store",
}

// sensitivePathReason reports why path must not be read or written through
// Loom's file tools, or "" when it is allowed. File contents go straight
// into the model conversation, i.e. to a third-party provider: credential
// files must never take that trip. The path is resolved (home expansion,
// absolute, symlinks) first, so "../../.aws/credentials" or a symlink named
// notes.txt pointing at a key does not get through.
func sensitivePathReason(path string) string {
	resolved := resolvePath(path)
	base := strings.ToLower(filepath.Base(resolved))

	if base == ".env" || (strings.HasPrefix(base, ".env.") && !isEnvTemplate(base)) {
		return "archivo .env con secretos"
	}
	if sensitiveBasenames[base] || strings.HasPrefix(base, "id_rsa") || strings.HasPrefix(base, "id_ed25519") || strings.HasPrefix(base, "id_ecdsa") {
		return "credencial o llave privada"
	}
	for _, ext := range sensitiveExtensions {
		if strings.HasSuffix(base, ext) {
			if strings.Contains(ext, "tfstate") {
				return "state de Terraform (contiene secretos en claro); usa `terraform state show <recurso>` o `terraform output`"
			}
			return "llave o certificado privado"
		}
	}
	slashed := filepath.ToSlash(resolved)
	if home, err := os.UserHomeDir(); err == nil {
		homeSlashed := filepath.ToSlash(filepath.Clean(home))
		for _, rel := range sensitiveHomePaths {
			target := homeSlashed + "/" + rel
			if pathEqualOrUnder(slashed, target) {
				return "directorio de credenciales (~/" + rel + ")"
			}
		}
	}
	for _, kubeconfig := range filepath.SplitList(os.Getenv("KUBECONFIG")) {
		if kubeconfig != "" && pathEqualOrUnder(slashed, filepath.ToSlash(resolvePath(kubeconfig))) {
			return "kubeconfig (contiene tokens del cluster)"
		}
	}
	return ""
}

func isEnvTemplate(base string) bool {
	for _, suffix := range []string{".example", ".sample", ".template", ".dist", ".defaults"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

func pathEqualOrUnder(path, target string) bool {
	if isCaseInsensitiveFS() {
		path, target = strings.ToLower(path), strings.ToLower(target)
	}
	return path == target || strings.HasPrefix(path, target+"/")
}

func resolvePath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[1:])
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return filepath.Clean(path)
}

func checkSensitivePath(path string) error {
	if reason := sensitivePathReason(path); reason != "" {
		return fmt.Errorf("ruta protegida (%s): Loom no lee ni escribe archivos de credenciales, porque su contenido iria al proveedor del modelo", reason)
	}
	return nil
}
