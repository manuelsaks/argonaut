# 🐙 Argonaut — Argo CD TUI

[![Github Downloads](https://img.shields.io/github/downloads/darksworm/argonaut/total?style=flat-square&label=github+downloads)](https://github.com/darksworm/argonaut/releases/latest)
[![License](https://img.shields.io/github/license/darksworm/argonaut?style=flat-square)](https://github.com/darksworm/argonaut/blob/main/LICENSE)
[![CodeRabbit](https://img.shields.io/coderabbit/prs/github/darksworm/argonaut?style=flat-square)](https://www.coderabbit.ai)

Argonaut is a keyboard-first terminal UI for **Argo CD**, built with **Bubbletea**. Browse apps, scope by clusters/namespaces/projects, stream live resource status, trigger syncs, inspect diffs in your favorite pager, and roll back safely — all without leaving your terminal.

> ❤️ 🐶
> &nbsp;Inspired by the great UX of [k9s](https://k9scli.io) — but for Argo CD.

---

## 📦 Prerequisites

- [**Argo CD CLI**](https://argo-cd.readthedocs.io/en/stable/cli_installation/) installed
- **Git** installed for generating diffs
- [**Delta**](https://dandavison.github.io/delta/installation.html) installed for enhanced diffs (optional, falls back to `git`)

---

## 🚀 Installation methods

### Windows (PowerShell)

Build with a current patched Go toolchain:

```powershell
$env:GOTOOLCHAIN = 'go1.26.10'
go build -o argonaut.exe ./cmd/app
.\argonaut.exe
```

The Windows TUI uses its built-in pager for diffs and logs; `less`, `sh`, Git
Bash and WSL are not required. Optional `delta` runs directly. Install native
`k9s` and `kubectl` to use their respective integrations. The development
Makefile/k3d scripts still require a Unix environment.

<details>
  <summary><strong>Install Script (Linux/macOS)</strong></summary>

```bash
curl -sSL https://raw.githubusercontent.com/darksworm/argonaut/main/install.sh | sh
```

The install script automatically detects your system (including musl vs glibc on Linux) and downloads the appropriate binary from the latest release.

You can also install a specific version:
```bash
curl -sSL https://raw.githubusercontent.com/darksworm/argonaut/main/install.sh | sh -s -- v1.13.0
```
</details>

<details>
  <summary><strong>Homebrew (Linux/MacOS)</strong></summary>

```bash
brew tap darksworm/homebrew-tap
brew install darksworm/tap/argonaut
```
</details>

<details>
  <summary><strong>AUR (Arch User Repository)</strong></summary>

```bash
yay -S argonaut-bin
```

Or, to track the latest commit on `main` (rebuilt from source on install):

```bash
yay -S argonaut-git
```
</details>

<details>
    <summary><strong>Nix</strong></summary>
With Nix and Flakes enabled, you can run or install Argonaut from nixpkgs or directly from the repository.

### nixpkgs

Argonaut is currently available on the `unstable` channel of [nixpkgs](https://search.nixos.org/packages?channel=unstable&query=argonaut&show=argonaut). So you can install it using

```bash
nix-env -iA nixos.argonaut
```

On other systems (macOS, non-NixOS Linux):

```bash
nix-env -iA nixpkgs.argonaut
```

or by adding it to your `systemPackages`.

```nix
environment.systemPackages = with pkgs; [ argonaut ];
```

### Run Directly

To run Argonaut without permanently installing it, use the `nix run` command:

```bash
nix run github:darksworm/argonaut
```

### Development Shell

To enter a development shell with Argonaut and its dependencies (like Go, Delta, and Argocd) available for development, use the `nix develop` command:

```bash
nix develop github:darksworm/argonaut
```

### Flakes

If you are using Flakes to manage your systems add this to your Flake:

```nix
inputs = {
  argonaut = {
    url = "github:darksworm/argonaut";
    inputs.nixpkgs.follows = "nixpkgs";
  };
  # ...
}
```

</details>

<details>
  <summary><strong>Docker</strong></summary>

Pull the image:
```bash
docker pull ghcr.io/darksworm/argonaut:latest
```

Run with mounted Argo CD config (as your host user to preserve permissions):
```bash
docker run -it --rm \
  -u $(id -u):$(id -g) \
  -v ~/.config/argocd:/home/appuser/.config/argocd:ro \
  ghcr.io/darksworm/argonaut:latest
```

The container needs access to your Argo CD configuration for authentication. The `:ro` flag mounts it as read-only for security.
</details>

[//]: # (</details>)

[//]: # (<details>)

[//]: # (  <summary><strong>NUR &#40;Nix User Repository&#41;</strong></summary>)

[//]: # ()
[//]: # (```bash)

[//]: # (nix-env -iA nur.repos.darksworm.argonaut)

[//]: # (```)

[//]: # (</details>)

<details>
  <summary><strong>Download a binary</strong></summary>

You can download binaries and packages in from the [**latest release**](https://github.com/darksworm/argonaut/releases/latest).

</details>

## ⚡ Quickstart
```bash
# Log in to your Argo CD server
argocd login

# Start Argonaut
argonaut
```

Press `c` (or enter `:ctx`) to switch between contexts saved by the Argo CD CLI
while Argonaut is running. Use the arrow keys or `j`/`k`, then `Enter` to switch;
`/` filters the list and `Esc` returns to apps. The active context is marked `*`.
The list is refreshed from the Argo CD CLI configuration each time it opens.
Argonaut uses that context's server and token directly, without kubeconfig, and
does not change the CLI's `current-context`. Core and port-forward contexts are
not supported for live switching. You can also switch directly with `:ctx NAME`.

The header shows the active Argo CD context, current view and the shortcuts
available in that view, in a layout inspired by k9s. Press `0`/`c` for contexts,
`1` for apps, `2` for destination servers, `3` for namespaces, `4` for projects and `5` for
ApplicationSets. `:` opens commands and `?` shows full help. View shortcuts do
not intercept text input or bypass confirmation dialogs.

Argo CD starts in the context picker. Saved automatic SSO for the current context
starts immediately. The picker remains usable while the CLI waits for browser/MFA
completion; failed or missing authentication returns here instead of an error screen.
Enter opens the selected context. `0` or `c` also opens the picker from error views.

For SSO, open contexts and press `Ctrl+A` on a row to toggle **auto-login** for that
context. Preferences persist in Argonaut's config. Selecting an enabled context
checks/renews its session before loading. Selecting `all` does this for every
enabled context, serially, so browser callbacks and CLI config writes cannot clash.
On an authentication failure in any Argo CD view,
Argonaut first asks the Argo CD CLI to refresh the session silently. If a new SSO
login is necessary, it runs `argocd relogin` in the background, which opens your
browser, then reloads credentials. The TUI keeps handling keys during SSO.
Press `Ctrl+L` to log in
manually for the selected context (or the active context outside the picker).
SSO/MFA can still require confirmation in the browser. Failed or cancelled login
attempts are throttled; interrupted sync/delete actions are never retried.

Auto-login is off until enabled for a context. Argonaut stores no passwords;
credentials remain in the Argo CD CLI config. The CLI must be installed. If it
is not on PATH, set an executable path (without shell arguments), for example:

```toml
[auth]
command = 'C:\Tools\argocd.exe'

[auth.auto_relogin]
development = true
production = false
```

---

## ✨ Highlights

- **Instant app browsing** with live updates (NDJSON streams)
- **Scoped navigation**: clusters → namespaces → projects → apps
- **Command palette** (`:`) for actions: `sync`, `diff`, `rollback`, `resources`, etc.
- **Live resources view** per app with health & sync status
- **Events & sync status pane** beside the resource tree — Kubernetes events and the last sync operation for whatever row you're on, auto-refreshing
- **External diff integration**: prefers `delta`, falls back to `git --no-index diff | less`
- **Guided rollback** with revision metadata and progress streaming
- **Execute actions** on resources — dynamically discovered per resource, including Argo Rollouts and any custom-defined actions
- **Keyboard-only workflow** with Vim-like navigation

---

## 📸 Screenshots

### **Live Apps**  
<img src="assets/argonaut_apps.png" alt="Apps list"/>

### **Sync**
<img src="assets/argonaut_sync.png" alt="Sync apps"/>

### **Live resource tree and events**
<img src="assets/argonaut_events.gif" alt="Events and sync status pane following the tree selection"/>

### **Diff**  
<img src="assets/argonaut_diff.png" alt="External diff"/>

### **Rollback**  
<img src="assets/argonaut_rollback.png" alt="Rollback flow"/>

### **Delete apps**  
<img src="assets/argonaut_delete_apps.png" alt="Delete apps"/>

### **Execute actions**  
<img src="assets/argonaut_resource_actions.png" alt="Execute actions modal — dynamically lists actions for the selected resource"/>

### **Enjoy colorful themes**  
<img src="assets/argonaut_themes.gif" alt="Many themes to choose from"/>

### **And much more**  
<img src="assets/argonaut_help.png" alt="the :help command describes all commands"/>

## Inspecting applications and resources

Press `d` to describe the cursor's Application, ApplicationSet or tree resource,
and `y` to view its full YAML, including metadata and status. Both use Argo CD's
API and the built-in scrollable viewer; they do not require kubeconfig, kubectl,
less or sh. In the viewer, `d`/`y` switch between the same captured object,
`Ctrl+R` reloads it, `/` finds text, `h/l` pan and `Esc` returns. Describe omits
empty fields and verbose historical annotations. Its live-to-desired
diff appears in a separate colored pane on the right in wide terminals; Tab
switches pane focus (or switches panes in narrow terminals). YAML keys, values,
status and diff additions/removals are colored. Use / to find without removing
surrounding document lines, and n/N for the next/previous match. A missing resource
shows its
desired manifest when Argo CD provides one; runtime resources without a desired
manifest say so. Secret values follow Argo CD's access controls and masking.

Operations in apps and resources use Ctrl: `Ctrl+S` sync, `Ctrl+R` refresh,
`Ctrl+D` delete, `Ctrl+B` rollback, `Ctrl+T` terminate, `Ctrl+A` resource actions
(tree), and `Ctrl+K` open k9s. Confirmations still require an explicit choice.

Press `2` or `:servers` to list registered destination servers in the current
Argo CD context, including servers with no applications. Each row shows its
name, app count and address/project. The `all` row shows apps on all destination
servers; Enter opens a server's apps and `Ctrl+R` reloads
the registry. Registry access follows Argo CD RBAC. If it is denied, the view
states the error and retains known destinations rather than claiming a full list.
Apps show their ApplicationSet owner (or standalone) and destination server /
namespace. `5` shows ApplicationSets derived from app owners with app counts and
locations; Enter opens their generated apps. Same-named sets in different
Application namespaces remain separate. `?` opens scrollable help.

The context picker (`c`) also has an `all` option, available directly as
`:ctx all`. It aggregates Applications, ApplicationSets and destination servers
from saved Argo CD API contexts, with a CONTEXT column and context-qualified
identities (`context::namespace/name`). It refreshes every minute; Ctrl+R
reloads immediately. Failed or unsupported contexts are reported independently.
Core and port-forward contexts are excluded. YAML/describe use the selected
row's Argo CD server; actions and resource trees enter that context before
confirmation. Esc from its apps view returns to the overview. Batch operations
use selections from one context.

For a slow connection or VPN, run `argonaut --low-bandwidth` (PowerShell:
`.\argonaut.exe --low-bandwidth`, Git Bash: `./argonaut.exe --low-bandwidth`).
This replaces Application and resource-tree streams with snapshots every two
minutes, stops polling while an operation or inspection modal is open, and
disables automatic events-panel refresh and release checks. YAML, describe,
diff, events and actions remain available on demand. `Ctrl+R` still refreshes
immediately. Sync progress is sampled at the polling interval in this mode;
run without the flag for live streams.

All modes reuse TCP/TLS connections, negotiate HTTP/2 when supported, and accept
gzip responses. The registry is cached **in memory** for five minutes; manual
refresh bypasses it. Applications appear independently of the registry response.
Resource trees fetch only the Application's resource-status fields instead of
its entire spec and deployment history. Actual compression depends on the server
and proxy configuration.

To keep low-bandwidth mode enabled, add this to your Argonaut `config.toml`
(Windows: `%APPDATA%\argonaut\config.toml`):

```toml
[network]
low_bandwidth = true
refresh_interval = "2m"   # "0" disables automatic snapshot refresh

[http_timeouts]
request_timeout = "60s"   # Default with low_bandwidth; normal default is 10s
```

Without `low_bandwidth`, `network.refresh_interval` controls only the all-context
overview (default `"1m"`); single-context views retain live streams. An explicit
`http_timeouts.request_timeout` always takes precedence. Edit existing TOML
sections rather than adding duplicate section headers.

### Kargo

Open Kargo with **6** or `:kargo` / `:kg`. You can also start directly in
Kargo, without an Argo CD session or kubeconfig:

```powershell
kargo login https://kargo.example.com --sso
.\argonaut.exe --kargo --low-bandwidth
```

Kargo CLI must be installed for browser SSO and session renewal. Reads and
promotions use the Kargo API directly, sharing Argonaut's connection pools,
HTTP/2, compression and configured TLS trust. REST `v1beta1` / `v1alpha1`
and the older Connect JSON API are detected automatically. Credentials remain
in Kargo CLI's configuration;
Argonaut does not store passwords, bearer tokens or refresh tokens in its config.
The default `cli` instance reads `$XDG_CONFIG_HOME/kargo/config`, or
`~/.config/kargo/config` on Windows and Unix when XDG_CONFIG_HOME is unset.

| Key | Kargo action |
| --- | --- |
| `0` / `c` | Choose instance |
| `1` | Projects; Enter chooses a project |
| `2` | Stages; Enter shows that stage's Promotions |
| `3` | Promotions: phase, Stage, revision/artifacts, creation date, duration and errors |
| `4` | Warehouses: status, latest Freight, subscriptions and errors |
| `5` | Freight: alias, origin and artifacts |
| `d` / `y` | Compact describe / full YAML |
| `Ctrl+P` | Select Stage and Freight, then open promotion confirmation |
| `Ctrl+T` | Retry a Failed, Errored or Aborted Promotion (list or describe); press again to confirm |
| `Ctrl+Y` | Copy the whole displayed YAML/describe document or focused diff pane |
| `Ctrl+R` | Fetch fresh data immediately |
| `Ctrl+L` | Browser SSO for the current/selected instance |
| `Ctrl+A` | Enable/disable automatic SSO for that instance |
| `Ctrl+E` | Open the complete last request error |
| `/` | Filter; Enter applies it immediately |
| `a` | Return to Argo CD |

For a first login without a saved server, enter `:login https://kargo.example.com`
in the Kargo view. `:login` reuses the current server. `:ctx`, `:projects`,
`:stages`, `:promotions`, `:warehouses`, `:freight`, `:refresh` and
`:argo` provide command navigation inside Kargo. `?` opens help.

To promote, select a Stage, press Ctrl+P, select Freight and press Enter.
Review the **instance, server, project, Stage and Freight**; a second Ctrl+P
submits the request once. The resulting Promotion opens with its phase,
step execution results and error messages. Its status refreshes until terminal;
Ctrl+R also refreshes manually. Esc cancels before submission. A lost response
never causes an automatic retry: check Promotions before submitting again.
Server RBAC and promotion eligibility checks remain authoritative.

Retry follows the [Kargo UI](https://github.com/akuity/kargo/blob/main/ui/src/features/stage/promotions.tsx):
it creates a new Promotion for the original Freight and Stage, using the current
Stage template. It starts a new run; it does not resume the failed step.
The original Promotion is re-read before submission to reject stale selections.

Promotion tables allocate column widths from the loaded data, giving long names
and artifact revisions priority. They show Git SHA/tag, image tag/digest and Helm
chart version when present in the target Freight reference embedded in Promotion
status. Missing revisions remain `-`; the Freight ID is shown separately.
Creation dates use local time. The selected row preview and `d` describe also show
start/end timestamps and duration. These fields require no additional API requests.
Promotion actions are green, retry actions yellow and failure states red.

Normal mode polls only the visible data every 10 seconds. Low-bandwidth mode
uses the global network interval (two minutes by default); `refresh_interval = "0"`
disables periodic refresh. Project lists are cached in memory for five minutes,
other lists for the refresh interval. YAML/describe open from the already loaded
object; older Connect servers fetch a raw JSON object for complete inspection.
Background requests pause during input/confirmation, Argo CD watchers
stop while browsing Kargo, and context changes cancel pending reads. No other
projects or instances are polled in the background. Promotion/status details remain
available via manual refresh when polling is disabled.

To configure multiple instances, add preferences to Argonaut's config:

```toml
[kargo]
command = "kargo"   # Or an absolute path to kargo.exe

[kargo.connections.production]
server = "https://kargo.prod.example.com"
auto_relogin = true
# callback_port = 8085  # Optional: use a redirect port registered with your IdP

[kargo.connections.staging]
server = "https://kargo.stage.example.com"
auto_relogin = false
# config_home = "C:/Users/you/.config"  # Optional: use an existing CLI session
```

Each named connection gets a separate CLI config home under Argonaut's config
directory unless `config_home` is explicit. Log in with Ctrl+L for each one.
Use a CLI release compatible with each server; a connection can override
`command = "C:/tools/kargo-legacy.exe"` when instances run different API generations.
SSO renewal uses the official CLI; failed/cancelled browser logins are throttled,
and a promotion is never replayed after login. Only HTTP 401 triggers automatic
login; HTTP 403 is shown as an RBAC error and stops background retries.
See the official [Kargo SSO login documentation](https://docs.kargo.io/user-guide/how-to-guides/logging-in).

Mouse selection is handled by the terminal by default, as in k9s. Drag to select
and use the terminal's Copy shortcut (Ctrl+Shift+C in Windows Terminal,
Ctrl+Insert in Git Bash/mintty). Ctrl+Y inside YAML/describe copies the whole
document using the native Windows Unicode clipboard. Clipboard failures are
reported instead of displaying a false success.

To opt into Argonaut's own selection and automatic copy on release:

```toml
[clipboard]
mouse_selection = true
```

In this mode the selected screen stays stable during watch updates; Ctrl+C or
Ctrl+Y copies the selection again, and Esc clears it.

ApplicationSets are fetched directly from Argo CD, including sets with no
generated Applications. The registry is cached for five minutes;
Ctrl+R in the AppSets view bypasses the cache. API/RBAC errors appear in the view.

## Advanced Features

Application commands accept `namespace/name`, for example `:delete team-a/my-app`
or `:sync team-a/my-app`. An unqualified name is accepted only when it identifies
one application. Autocomplete qualifies duplicate names, and checkbox selections
retain the Application namespace for batch operations.

### Client certificate authentication
Argonaut supports client certificate authentication. You just need to pass a couple arguments to the argonaut command:

```bash
argonaut --client-cert=/path/to/cert --client-cert-key=/path/to/key
```

### Self-signed certificates
If your Argo CD server uses a self-signed certificate, you can provide a custom CA certificate to trust:

```bash
argonaut --ca-cert=/path/to/ca.crt
```

### Port-forward mode

If your Argo CD server isn't directly accessible (e.g., running in a private cluster), Argonaut can connect via kubectl port-forward:

```bash
# Configure ArgoCD CLI for port-forward mode
argocd login --port-forward --port-forward-namespace argocd

# Start Argonaut (automatically detects port-forward mode)
argonaut
```

**Requirements:**
- `kubectl` configured with access to the cluster
- ArgoCD server pod running in the target namespace

**Custom namespace:** If ArgoCD is installed in a different namespace, add to your config:
```toml
# ~/.config/argonaut/config.toml
[port_forward]
namespace = "my-argocd-namespace"
```

---

## ⚙️ Configuration

Argonaut stores its configuration in a TOML file at `~/.config/argonaut/config.toml` (or `$XDG_CONFIG_HOME/argonaut/config.toml`).

You can override the config path with the `ARGONAUT_CONFIG` environment variable.

### Example Configuration

```toml
[appearance]
theme = "tokyo-night"

[appearance.overrides]
# Override individual theme colors (hex format)
# accent = "#ff79c6"
# success = "#50fa7b"

[sort]
field = "name"      # name, sync, health
direction = "asc"   # asc, desc

[k9s]
command = "k9s"           # Path to k9s executable
context = ""              # Override Kubernetes context for k9s

[diff]
viewer = ""               # Interactive diff viewer (e.g., "code --wait --diff {left} {right}", "meld {left} {right}")
formatter = ""            # Diff formatter command (e.g., "delta --side-by-side")

[events]
refresh_interval = "10s"  # How often the events/status pane refetches ("0" disables)
auto_open = true          # Open the pane automatically in the resource tree view

[http_timeouts]
request_timeout = "10s"   # Timeout for HTTP requests (increase for large deployments)

[updates]
check_enabled = true      # Set to false to disable the GitHub release-check on startup

# Start in clusters view instead of apps (supports :command syntax)
default_view = "clusters"
```

### Configuration Options

#### `[appearance]`

| Option | Description | Default |
|--------|-------------|---------|
| `theme` | Color theme name (see available themes below) | `tokyo-night` |

**Available themes:**
- **Dark themes**: `catppuccin-mocha`, `dracula`, `gruvbox-dark`, `monokai`, `nord`, `one-dark`, `oxocarbon`, `solarized-dark`, `tokyo-night`, `tokyo-storm`
- **Light themes**: `catppuccin-latte`, `gruvbox-light`, `one-light`, `onehalf-light`, `solarized-light`
- **Accessibility**: `colorblind-safe`, `grayscale-lowchroma`, `high-contrast`
- **Special**: `inherit-terminal` (uses your terminal's ANSI color palette)

You can also change the theme at runtime using the `:theme <name>` command.

#### `[appearance.overrides]`

Override individual theme colors with hex values. Available color keys:
- `accent`, `warning`, `dim`, `success`, `danger`, `progress`, `unknown`, `info`, `text`, `gray`
- `selected_bg`, `cursor_selected_bg`, `cursor_bg`, `border`, `muted_bg`, `shade_bg`, `dark_bg`

#### `[sort]`

| Option | Description | Default |
|--------|-------------|---------|
| `field` | Sort field (`name`, `sync`, `health`) | `name` |
| `direction` | Sort direction (`asc`, `desc`) | `asc` |

You can also change sorting at runtime using the `:sort <field> <direction>` command.

#### `[k9s]`

Integration settings for [k9s](https://k9scli.io), the Kubernetes TUI.

| Option | Description | Default |
|--------|-------------|---------|
| `command` | Path to k9s executable | `k9s` |
| `context` | Override Kubernetes context when launching k9s | (none) |

Press `K` on a resource in the tree view to open it in k9s.

#### `[diff]`

Settings for diff viewing and formatting.

| Option | Description | Default |
|--------|-------------|---------|
| `viewer` | Interactive diff viewer command. Use `{left}` and `{right}` as placeholders for file paths. The command must wait until the files are no longer needed (e.g., `code --wait --diff`); temporary manifests are deleted when it exits. | (none) |
| `formatter` | Non-interactive diff formatter piped through before display | (none, falls back to `delta` if installed) |

**Examples:**

```toml
[diff]
# Use VS Code as diff viewer
viewer = "code --diff {left} {right} --wait"

# Use meld as diff viewer
viewer = "meld {left} {right}"

# Use delta with custom options
formatter = "delta --side-by-side --line-numbers --paging=never"
```

If no `viewer` is set, Windows uses the built-in pager; other platforms use
`less -R` when available and the built-in pager otherwise. Navigate with `j/k`,
Page Up/Down, `g/G`, and `h/l`, search with `/`, and return with `Esc` or `q`.
If no `formatter` is set but [delta](https://dandavison.github.io/delta/) is
installed, it will be used automatically without a shell.

Custom viewer/formatter commands use PowerShell on Windows and `sh` on Unix;
use command syntax and executables appropriate for that platform. Quote an
executable path containing spaces, for example:

```toml
[diff]
viewer = "'C:\\Tools Folder\\viewer.exe' {left} {right}"
```

#### `[events]`

Settings for the events/status pane shown next to the resource tree.

| Option | Description | Default |
|--------|-------------|---------|
| `refresh_interval` | How often the pane refetches events and sync status. Use Go duration format (e.g., "30s", "1m"); `"0"` disables auto-refresh. | `"10s"` |
| `auto_open` | Open the pane automatically when the resource tree view opens. Set to `false` to open it on demand with `e`. | `true` |

At runtime, `:events on|off` changes this for the current session; add `always` (e.g. `:events off always`) to persist the choice here.

#### `[http_timeouts]`

Settings for HTTP request timeouts. Useful for large deployments with thousands of applications where API responses take longer.

| Option | Description | Default |
|--------|-------------|---------|
| `request_timeout` | Timeout for all HTTP requests to ArgoCD API. Use Go duration format (e.g., "30s", "1m", "90s") | `"10s"` |

**Examples:**

```toml
[http_timeouts]
# For large deployments with thousands of applications
request_timeout = "60s"

# For very large deployments
request_timeout = "2m"
```

> **Note:** If you're experiencing timeout errors when listing applications or resources, increase this value. The timeout applies to all API operations including listing applications, getting resources, and sync operations.

#### `[updates]`

Settings for the automatic update check. On startup (and once per hour after that), Argonaut hits the GitHub Releases API to see whether a newer version exists; when one does, it shows a `New version available, run :upgrade` hint in the status bar.

| Option | Description | Default |
|--------|-------------|---------|
| `check_enabled` | Enable the periodic GitHub release check. Set to `false` to disable in air-gapped environments, behind restrictive firewalls, or anywhere outbound traffic to `api.github.com` is unwanted. | `true` |

```toml
[updates]
check_enabled = false
```

#### `default_view`

Configure which view Argonaut starts in. Uses the same syntax as `:commands`, with an optional scope argument to drill down into a specific cluster, namespace, project, or application set.

| Value | Startup view |
|-------|-------------|
| `"apps"` | Applications list (default) |
| `"clusters"` | Clusters list |
| `"ns"` | Namespaces list |
| `"proj"` | Projects list |
| `"appsets"` | ApplicationSets list |
| `"cluster production"` | Namespaces scoped to cluster "production" |
| `"ns my-namespace"` | Projects scoped to namespace "my-namespace" |
| `"project myproj"` | Apps scoped to project "myproj" |
| `"appset myset"` | Apps scoped to ApplicationSet "myset" |

All view aliases from `:commands` are supported (e.g., `app`/`apps`/`applications`, `cls`/`cluster`/`clusters`, `ns`/`namespace`/`namespaces`, etc.).

**Examples:**

```toml
# Start in apps view
default_view = "apps"

# Start scoped to a cluster (shows its namespaces)
default_view = "cluster production"

# Start scoped to a namespace (shows its projects)
default_view = "ns my-namespace"
```

#### `[port_forward]`

Settings for port-forward mode (when ArgoCD CLI is configured with `server: port-forward`).

| Option | Description | Default |
|--------|-------------|---------|
| `namespace` | Kubernetes namespace where ArgoCD is installed | `argocd` |

---

## 🤝 Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on how to contribute to this project.
