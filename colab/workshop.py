"""Run the DevFest Ilorin 2026 observability workshop inside Google Colab.

Colab has no Docker, so this module does natively what compose.yaml does:

  * Postgres from apt (Ubuntu's package),
  * the same Collector, Prometheus, Tempo, Loki and Grafana versions that
    grafana/otel-lgtm:0.35.0 bundles, with that image's own config files
    (colab/lgtm/), downloaded from each project's official release,
  * every branch's Go services, built once into /content/bin/<branch>/.

The repo's Grafana provisioning, alert rules and service code run unchanged:
/etc/hosts gives each compose service name (postgres, lgtm, ledger, ...) the
address 127.0.0.1, and /devfest/grafana points at the current branch's
grafana/ folder, exactly where compose mounts it.

The one difference: Colab's own web server already holds port 8080, so the
transfers API listens on 18080 here.

Notebook usage:  import workshop as w; w.setup(); w.stage("01-metrics"); ...
"""

from __future__ import annotations

import concurrent.futures
import json
import os
import random
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import threading
import time
import urllib.parse
import urllib.request
import zipfile
from pathlib import Path

# --- layout and versions ----------------------------------------------------

ROOT = Path(os.environ.get("WORKSHOP_ROOT", "/content"))
REPO_URL = os.environ.get("WORKSHOP_REPO", "https://github.com/intellect4all/devfest-ilorin-2026-observability.git")
REPO = ROOT / "devfest"          # the clone
TREES = ROOT / "branches"        # one git worktree per branch
BIN = ROOT / "bin"               # one folder of binaries per branch
OPT = ROOT / "opt"               # Go and the observability stack
LOGS = ROOT / "logs"
DATA = Path("/data")             # same paths as otel-lgtm's configs
STATE = ROOT / "workshop-state.json"

BRANCHES = ["main", "01-metrics", "02-traces", "03-logs", "04-alerts", "hunt", "solution"]

GO = "1.26.8"
DOWNLOADS = {
    # name: (url, what to keep)  versions = grafana/otel-lgtm:0.35.0
    "go": f"https://go.dev/dl/go{GO}.linux-amd64.tar.gz",
    "prometheus": "https://github.com/prometheus/prometheus/releases/download/v3.15.0/prometheus-3.15.0.linux-amd64.tar.gz",
    "tempo": "https://github.com/grafana/tempo/releases/download/v3.0.3/tempo_3.0.3_linux_amd64.tar.gz",
    "loki": "https://github.com/grafana/loki/releases/download/v3.7.8/loki-linux-amd64.zip",
    "otelcol": "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.161.0/otelcol-contrib_0.161.0_linux_amd64.tar.gz",
    "grafana": "https://dl.grafana.com/oss/release/grafana-13.2.2.linux-amd64.tar.gz",
}

# Host ports. compose.yaml uses the same ones, except transfers (8080 is Colab's).
P = dict(transfers=18080, ledger=8081, provider=8082, alertrelay=8090,
         transfers_admin=7080, ledger_admin=7081, provider_admin=7082,
         grafana=3000, prometheus=9090, tempo=3200, loki=3100, otlp=4318)
PROM = f"http://127.0.0.1:{P['prometheus']}"
TEMPO = f"http://127.0.0.1:{P['tempo']}"
LOKI = f"http://127.0.0.1:{P['loki']}"
GRAFANA = f"http://127.0.0.1:{P['grafana']}"
T_ADMIN = f"http://127.0.0.1:{P['transfers_admin']}/admin/bugs"
L_ADMIN = f"http://127.0.0.1:{P['ledger_admin']}/admin/bugs"
P_ADMIN = f"http://127.0.0.1:{P['provider_admin']}/admin/config"

# Seed size. n2 (8 cores) used 7,000,000 rows; Colab has 2 vCPUs.
ROWS = int(os.environ.get("WORKSHOP_ROWS", "3000000"))
HISTORY = int(os.environ.get("WORKSHOP_HISTORY", "80000"))
LOAD_RATE = 20
SURGE_RATE = int(os.environ.get("WORKSHOP_SURGE_RATE", "60"))

try:  # Colab-only features: port proxy, opening tabs
    from google.colab import output as _colab_output  # type: ignore
    IN_COLAB = True
except ImportError:
    _colab_output = None
    IN_COLAB = False

_procs: dict[str, subprocess.Popen] = {}
_built: dict[str, threading.Event] = {b: threading.Event() for b in BRANCHES}
_build_errors: dict[str, str] = {}
_state: dict = {}


# --- small helpers ----------------------------------------------------------

def say(msg: str) -> None:
    print(msg, flush=True)


def sh(cmd: str, check: bool = True, quiet: bool = True, env: dict | None = None, cwd=None) -> str:
    r = subprocess.run(["bash", "-c", cmd], capture_output=True, text=True, env=env, cwd=cwd)
    if check and r.returncode != 0:
        raise RuntimeError(f"command failed ({r.returncode}): {cmd}\n{r.stdout[-2000:]}\n{r.stderr[-2000:]}")
    if not quiet:
        print(r.stdout, end="")
    return r.stdout


def http(method: str, url: str, body: bytes | None = None, headers: dict | None = None, timeout: float = 15):
    req = urllib.request.Request(url, data=body, method=method, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def get_json(url: str, params: dict | None = None, timeout: float = 15):
    if params:
        url += "?" + urllib.parse.urlencode(params)
    status, text = http("GET", url, headers={"Accept": "application/json"}, timeout=timeout)
    if status != 200:
        raise RuntimeError(f"GET {url}: HTTP {status}: {text[:300]}")
    return json.loads(text)


def port_open(port: int) -> bool:
    with socket.socket() as s:
        s.settimeout(0.5)
        return s.connect_ex(("127.0.0.1", port)) == 0


def wait_http(name: str, url: str, timeout: float = 90) -> None:
    t0 = time.time()
    while time.time() - t0 < timeout:
        try:
            if http("GET", url, timeout=3)[0] == 200:
                return
        except Exception:
            pass
        time.sleep(1)
    log = LOGS / f"{name}.log"
    tail = log.read_text()[-1500:] if log.exists() else ""
    raise RuntimeError(f"{name} did not become ready at {url} within {timeout:.0f}s\n{tail}")


def start(name: str, argv: list[str], env: dict | None = None, cwd=None) -> subprocess.Popen:
    """Start a long-running process in the background, logging to logs/<name>.log."""
    stop(name)
    LOGS.mkdir(parents=True, exist_ok=True)
    full_env = {**os.environ, **(env or {})}
    f = open(LOGS / f"{name}.log", "ab")
    p = subprocess.Popen(argv, stdout=f, stderr=subprocess.STDOUT, env=full_env, cwd=cwd,
                         start_new_session=True)
    _procs[name] = p
    (LOGS / f"{name}.pid").write_text(str(p.pid))
    return p


def stop(name: str) -> None:
    pidfile = LOGS / f"{name}.pid"
    pids = set()
    if name in _procs:
        pids.add(_procs.pop(name).pid)
    if pidfile.exists():
        try:
            pids.add(int(pidfile.read_text()))
        except ValueError:
            pass
        pidfile.unlink()
    for pid in pids:
        try:
            os.killpg(pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            continue
        for _ in range(50):
            try:
                os.kill(pid, 0)
            except ProcessLookupError:
                break
            try:  # reap if it is our child
                os.waitpid(pid, os.WNOHANG)
            except ChildProcessError:
                pass
            time.sleep(0.1)
        else:
            try:
                os.killpg(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass


def running(name: str) -> bool:
    pidfile = LOGS / f"{name}.pid"
    if not pidfile.exists():
        return False
    try:
        os.kill(int(pidfile.read_text()), 0)
        return True
    except (ProcessLookupError, ValueError):
        return False


def _save_state() -> None:
    ROOT.mkdir(parents=True, exist_ok=True)
    STATE.write_text(json.dumps(_state))


def _load_state() -> None:
    if STATE.exists():
        _state.update(json.loads(STATE.read_text()))


# --- setup ------------------------------------------------------------------

def _download(name: str) -> None:
    dest = OPT / name
    if (dest / ".done").exists():
        return
    url = DOWNLOADS[name]
    OPT.mkdir(parents=True, exist_ok=True)
    archive = OPT / Path(urllib.parse.urlparse(url).path).name
    if not archive.exists():
        tmp = archive.with_suffix(".part")
        urllib.request.urlretrieve(url, tmp)
        tmp.rename(archive)
    shutil.rmtree(dest, ignore_errors=True)
    dest.mkdir(parents=True)
    if archive.suffix == ".zip":
        with zipfile.ZipFile(archive) as z:
            z.extractall(dest)
    else:
        sh(f"tar -xzf {archive} -C {dest}")
    archive.unlink()
    for f in dest.rglob("*"):  # zip extraction drops the executable bit
        if f.is_file() and f.name in {"loki-linux-amd64", "tempo", "prometheus", "otelcol-contrib", "grafana"}:
            f.chmod(0o755)
    (dest / ".done").touch()


def _find(name: str, binary: str) -> str:
    hits = [p for p in (OPT / name).rglob(binary) if p.is_file() and os.access(p, os.X_OK)]
    if not hits:
        raise RuntimeError(f"{binary} not found in {OPT / name}")
    return str(sorted(hits, key=lambda p: len(str(p)))[0])


def _postgres() -> None:
    if not shutil.which("pg_ctlcluster"):
        sh("export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get install -y -qq postgresql >/dev/null")
    ver = sorted(os.listdir("/usr/lib/postgresql"))[-1]
    if "online" not in sh("pg_lsclusters", check=False):
        sh(f"pg_ctlcluster {ver} main start")
    q = lambda s: sh(f"su postgres -c \"psql -qtAX -c \\\"{s}\\\"\"")
    if q("SELECT 1 FROM pg_roles WHERE rolname='devfest'").strip() != "1":
        q("CREATE ROLE devfest LOGIN PASSWORD 'devfest'")
        q("CREATE DATABASE devfest OWNER devfest")
        # compose.yaml: single-threaded scans, 1 GB of shared buffers
        q("ALTER SYSTEM SET max_parallel_workers_per_gather = 0")
        q("ALTER SYSTEM SET shared_buffers = '1GB'")
        sh(f"pg_ctlcluster {ver} main restart")
    _state["postgres"] = ver


def _go_env() -> dict:
    return {**os.environ, "PATH": f"{OPT}/go/go/bin:" + os.environ["PATH"], "CGO_ENABLED": "0",
            "GOCACHE": str(ROOT / ".gocache"), "GOMODCACHE": str(ROOT / ".gomod"), "GOFLAGS": "-modcacherw",
            "HOME": os.environ.get("HOME", "/root")}


def _clone() -> None:
    if not (REPO / ".git").exists():
        sh(f"git clone -q {REPO_URL} {REPO}")
    else:
        sh(f"git -C {REPO} fetch -q origin")
    TREES.mkdir(parents=True, exist_ok=True)
    for b in BRANCHES:
        tree = TREES / b
        if tree.exists():
            sh(f"git -C {tree} checkout -q --detach origin/{b}")
        else:
            sh(f"git -C {REPO} worktree add -q --detach {tree} origin/{b}")


def _build_all() -> None:
    env = _go_env()
    for b in BRANCHES:
        out = BIN / b
        try:
            rev = sh(f"git -C {TREES / b} rev-parse --short HEAD").strip()
            if (out / ".rev").exists() and (out / ".rev").read_text() == rev:
                _built[b].set()
                continue
            shutil.rmtree(out, ignore_errors=True)
            out.mkdir(parents=True)
            sh(f"go build -trimpath -ldflags='-s -w' -o {out}/ ./...", env=env, cwd=TREES / b)
            (out / ".rev").write_text(rev)
        except Exception as e:  # noqa: BLE001 - reported by stage()
            _build_errors[b] = str(e)
        _built[b].set()


_build_thread: threading.Thread | None = None


def _ensure_build() -> None:
    """Start the background build once per kernel (it skips branches already built)."""
    global _build_thread
    if _build_thread is None or not _build_thread.is_alive():
        if not all(e.is_set() for e in _built.values()):
            _build_thread = threading.Thread(target=_build_all, daemon=True)
            _build_thread.start()


def _hosts() -> None:
    names = "postgres lgtm ledger provider transfers alertrelay"
    hosts = Path("/etc/hosts")
    if "devfest-workshop" not in hosts.read_text():
        with hosts.open("a") as f:
            f.write(f"127.0.0.1 {names}  # devfest-workshop\n")


def _grafana_url() -> str:
    """The address the viewer's browser uses for Grafana."""
    if IN_COLAB:
        try:
            return _colab_output.eval_js(f"google.colab.kernel.proxyPort({P['grafana']})")
        except Exception:  # noqa: BLE001
            pass
    return GRAFANA + "/"


def _start_lgtm() -> None:
    cfg = REPO / "colab" / "lgtm"
    for d in ["prometheus", "tempo", "loki", "grafana/data", "grafana/plugins"]:
        (DATA / d).mkdir(parents=True, exist_ok=True)
    if not running("otelcol"):
        start("otelcol", [_find("otelcol", "otelcol-contrib"), f"--config=file:{cfg}/otelcol-config.yaml"])
    if not running("prometheus"):
        start("prometheus", [_find("prometheus", "prometheus"), "--web.enable-remote-write-receiver",
                             "--web.enable-otlp-receiver", "--enable-feature=exemplar-storage",
                             f"--storage.tsdb.path={DATA}/prometheus", f"--config.file={cfg}/prometheus.yaml"])
    if not running("tempo"):
        start("tempo", [_find("tempo", "tempo"), f"--config.file={cfg}/tempo-config.yaml"])
    if not running("loki"):
        start("loki", [_find("loki", "loki-linux-amd64"), f"--config.file={cfg}/loki-config.yaml"])
    if not running("grafana"):
        gbin = _find("grafana", "grafana")
        home = str(Path(gbin).parent.parent)
        root_url = _state["grafana_url"]
        start("grafana", [gbin, "server", f"--homepath={home}"], cwd=home, env={
            # grafana/otel-lgtm's run-grafana.sh
            "GF_AUTH_ANONYMOUS_ENABLED": "true", "GF_AUTH_ANONYMOUS_ORG_ROLE": "Admin",
            "GF_PATHS_DATA": f"{DATA}/grafana/data", "GF_PATHS_PLUGINS": f"{DATA}/grafana/plugins",
            # compose.yaml
            "GF_PLUGINS_PREINSTALL_DISABLED": "true", "GF_DASHBOARDS_MIN_REFRESH_INTERVAL": "5s",
            "GF_ANALYTICS_REPORTING_ENABLED": "false", "GF_ANALYTICS_CHECK_FOR_UPDATES": "false",
            "GF_NEWS_NEWS_FEED_ENABLED": "false", "GF_PATHS_PROVISIONING": "/devfest/grafana/provisioning",
            # Colab: Grafana is reached through Colab's HTTPS port proxy
            "GF_SERVER_ROOT_URL": root_url,
        })
    for name, url in [("otelcol", "http://127.0.0.1:13133/ready"),
                      ("prometheus", f"{PROM}/api/v1/status/runtimeinfo"),
                      ("tempo", f"{TEMPO}/ready"), ("loki", f"{LOKI}/ready"),
                      ("grafana", f"{GRAFANA}/api/health")]:
        wait_http(name, url, timeout=120)


def setup(rows: int = ROWS, history: int = HISTORY) -> None:
    """Install and start everything, build every branch, load the seed data. About 5 minutes."""
    t0 = time.time()
    _load_state()
    if os.geteuid() != 0:
        raise RuntimeError("run this in Colab (it needs root to install Postgres)")
    owner = {"otlp": "otelcol", "transfers_admin": "transfers", "ledger_admin": "ledger",
             "provider_admin": "provider"}
    busy = [f"{n}:{p}" for n, p in P.items() if port_open(p) and not running(owner.get(n, n))]
    if busy and not _state.get("setup_done"):
        raise RuntimeError(f"ports already in use: {busy}. Runtime → Restart session, then run setup again.")
    _hosts()
    if not _state.get("ntfy_topic"):
        _state["ntfy_topic"] = "devfest-ilorin-" + secrets.token_hex(4)
    _state["grafana_url"] = _grafana_url()
    _save_state()

    say("1/5  downloading Go, Prometheus, Tempo, Loki, the Collector and Grafana; installing Postgres; cloning the repo")
    with concurrent.futures.ThreadPoolExecutor(8) as pool:
        jobs = {pool.submit(_download, n): n for n in DOWNLOADS}
        jobs[pool.submit(_postgres)] = "postgres"
        jobs[pool.submit(_clone)] = "git clone"
        for j in concurrent.futures.as_completed(jobs):
            j.result()
            say(f"     ✓ {jobs[j]}")

    say("2/5  building the seven branches in the background (main first)")
    _ensure_build()

    say("3/5  starting the Collector, Prometheus, Tempo, Loki and Grafana")
    _link_grafana("main")
    _start_lgtm()

    say("4/5  starting the services on main")
    stage("main", quiet=True)

    say(f"5/5  seeding {rows:,} ledger entries and {history:,} recent transfers")
    seed(rows, history)
    _state["setup_done"] = True
    _save_state()
    say(f"✓ ready in {time.time() - t0:.0f}s. Branches still building in the background; stage() waits for them.")
    grafana()


# --- the services -----------------------------------------------------------

def _link_grafana(branch: str) -> None:
    Path("/devfest").mkdir(exist_ok=True)
    target = TREES / branch / "grafana"
    tmp = Path("/devfest/grafana.new")
    if tmp.is_symlink() or tmp.exists():
        tmp.unlink()
    tmp.symlink_to(target)
    os.replace(tmp, "/devfest/grafana")


def _env(service: str) -> dict:
    otel = {"OTEL_EXPORTER_OTLP_ENDPOINT": "http://lgtm:4318",
            "OTEL_RESOURCE_ATTRIBUTES": "deployment.environment.name=devfest-stage"}
    return {
        "ledger": {**otel, "DATABASE_URL": "postgres://devfest:devfest@postgres:5432/devfest?pool_max_conns=8"},
        "provider": {**otel, "PROVIDER_LATENCY_MS": "35"},
        "transfers": {**otel, "PORT": str(P["transfers"]),
                      "DATABASE_URL": "postgres://devfest:devfest@postgres:5432/devfest?pool_max_conns=10",
                      "LEDGER_URL": "http://ledger:8081", "PROVIDER_URL": "http://provider:8082",
                      "PREFUNDED_ACCOUNTS": "acct-00001"},
        "alertrelay": {"NTFY_URL": "https://ntfy.sh", "NTFY_TOPIC": _state.get("ntfy_topic", ""),
                       "ALERT_CLICK_URL": _state.get("grafana_url", "")},
    }[service]


def stage(branch: str, quiet: bool = False) -> None:
    """Switch the running system to another branch, like make stage STAGE=<branch>."""
    if branch not in BRANCHES:
        raise ValueError(f"no branch called {branch}; pick one of {BRANCHES}")
    t0 = time.time()
    if not _built[branch].is_set():
        _ensure_build()
        say(f"waiting for {branch} to finish building…")
        _built[branch].wait()
    if branch in _build_errors:
        raise RuntimeError(f"building {branch} failed:\n{_build_errors[branch]}")
    bins = BIN / branch
    services = [s for s in ["ledger", "provider", "transfers", "alertrelay"] if (bins / s).exists()]
    for s in ["transfers", "alertrelay", "ledger", "provider"]:
        stop(s)
    for s in services:
        start(s, [str(bins / s)], env=_env(s))
    for s in services:
        wait_http(s, f"http://127.0.0.1:{P[s]}/healthz", timeout=60)
    _link_grafana(branch)
    for k in ["datasources", "dashboards", "alerting"]:
        status, body = http("POST", f"{GRAFANA}/api/admin/provisioning/{k}/reload",
                            headers={"Authorization": "Basic YWRtaW46YWRtaW4="})
        if status != 200:
            raise RuntimeError(f"Grafana {k} reload failed: HTTP {status} {body[:200]}")
    _state["stage"] = branch
    _changed()
    if not quiet:
        say(f"✓ on {branch} in {time.time() - t0:.1f}s (bugs A and C reset to off; bug B: {_get(L_ADMIN)})")


def seed(rows: int = ROWS, history: int = HISTORY) -> None:
    """Load the ledger and the recent-transfer history (make seed)."""
    t0 = time.time()
    sql = TREES / _state.get("stage", "main") / "scripts" / "seed.sql"
    out = sh(f"PGPASSWORD=devfest psql -q -h 127.0.0.1 -U devfest -d devfest -v rows={rows} -v history={history} -f {sql}")
    # Bug A's history is read at start-up: restart the services so they see it.
    stage(_state.get("stage", "main"), quiet=True)
    say(f"     seeded in {time.time() - t0:.0f}s{(': ' + out.strip().splitlines()[-1]) if out.strip() else ''}")


def _get(url: str) -> str:
    try:
        return http("GET", url, timeout=5)[1].strip()
    except Exception:  # noqa: BLE001
        return "?"


def _post(url: str) -> str:
    status, body = http("POST", url, timeout=5)
    if status != 200:
        raise RuntimeError(f"POST {url}: HTTP {status} {body}")
    return body.strip()


# --- traffic ----------------------------------------------------------------

def _loadgen_bin() -> str:
    return str(BIN / _state.get("stage", "main") / "loadgen")


def load(rate: int = LOAD_RATE, hot: float = 0.9) -> None:
    """Steady traffic in the background (make load)."""
    start("loadgen", [_loadgen_bin(), f"-url=http://transfers:{P['transfers']}/transfers",
                      f"-rate={rate}", f"-hot={hot}"])
    say(f"loadgen: {rate} req/s, {hot:.0%} from the settlement wallet acct-00001")


def load_stop() -> None:
    stop("loadgen")
    say("loadgen stopped")


def surge(rate: int = SURGE_RATE, seconds: int = 90) -> None:
    """Extra traffic on top for a while (make surge); runs in the background."""
    start("surge", [_loadgen_bin(), f"-url=http://transfers:{P['transfers']}/transfers",
                    f"-rate={rate}", f"-duration={seconds}s", "-hot=0", "-label=surge"])
    _changed()
    say(f"surge: +{rate} req/s for {seconds}s")


def load_logs(lines: int = 5) -> None:
    """The loadgen's own client-side numbers (make load-logs)."""
    f = LOGS / "loadgen.log"
    say("\n".join(f.read_text().splitlines()[-lines:]) if f.exists() else "no loadgen running: w.load()")


def tx(key: str | None = None, frm: str = "acct-00002", to: str = "acct-00003", amount: int = 2500) -> None:
    """One transfer (make tx). Pass the same key twice to see the idempotent replay."""
    key = key or str(__import__("uuid").uuid4())
    t0 = time.time()
    status, body = http("POST", f"http://127.0.0.1:{P['transfers']}/transfers",
                        json.dumps({"from": frm, "to": to, "amount": amount}).encode(),
                        {"Content-Type": "application/json", "Idempotency-Key": key})
    say(f"Idempotency-Key: {key}\n{body.strip()}\n  HTTP {status} in {time.time() - t0:.3f}s")


def service_logs(service: str = "transfers", lines: int = 8) -> None:
    """The last lines a service printed on stdout (docker compose logs)."""
    f = LOGS / f"{service}.log"
    say("\n".join(f.read_text(errors="replace").splitlines()[-lines:]) if f.exists() else f"{service} is not running")


# --- bugs -------------------------------------------------------------------

def bug(name: str, on: bool = True) -> None:
    """bug("c") turns bug C on, bug("c", False) turns it off."""
    name = name.lower()
    url = {"a": T_ADMIN, "b": L_ADMIN, "c": T_ADMIN}[name]
    _post(f"{url}?{name}={'on' if on else 'off'}")
    _changed()
    say(f"bug {name.upper()} {'on' if on else 'off'}")


def bugs_off() -> None:
    _post(f"{T_ADMIN}?a=off&c=off")
    _post(f"{L_ADMIN}?b=off")
    _post(f"{P_ADMIN}?error_rate=0&timeout_rate=0")
    stop("surge")
    _changed()
    say("all bugs off, provider normal")


def _bugs_state() -> dict:
    out = {}
    for url in (T_ADMIN, L_ADMIN):
        try:
            out.update(json.loads(_get(url)))
        except Exception:  # noqa: BLE001
            pass
    return out


def bugs() -> None:
    say(f"transfers: {_get(T_ADMIN)}  ledger: {_get(L_ADMIN)}  provider: {_get(P_ADMIN)}")


def provider_flaky(rate: float = 0.2) -> None:
    _post(f"{P_ADMIN}?error_rate={rate}")
    say(f"provider rejects {rate:.0%} of payouts")


def explain() -> None:
    """The balance query's plan: Index Scan is healthy, Seq Scan is bug B."""
    sh("PGPASSWORD=devfest psql -h 127.0.0.1 -U devfest -d devfest -c \"EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) "
       "SELECT COALESCE(SUM(amount), 0)::bigint FROM entries WHERE account_id = 'acct-00042';\"", quiet=False)


# --- looking at the signals from the notebook --------------------------------

def prom(q: str) -> float | None:
    r = get_json(f"{PROM}/api/v1/query", {"query": q})["data"]["result"]
    return float(r[0]["value"][1]) if r else None


def _p(q: float, window: str = "1m") -> str:
    v = prom(f'histogram_quantile({q}, sum by (le) (rate(http_server_request_duration_seconds_bucket{{job="transfers"}}[{window}])))')
    return "–" if v is None or v != v else f"{v * 1000:.0f}ms"


def snapshot() -> dict:
    """Key numbers over the last minute, the same ones make verify records."""
    t = 'job="transfers"'
    s = {
        "p50": _p(0.5), "p99": _p(0.99),
        "req/s": prom(f'sum(rate(http_server_request_duration_seconds_count{{{t}}}[1m]))'),
        "5xx %": prom(f'100 * (sum(rate(http_server_request_duration_seconds_count{{{t},http_response_status_code=~"5.."}}[1m])) or vector(0)) / sum(rate(http_server_request_duration_seconds_count{{{t}}}[1m]))'),
        "goroutines": prom(f'max_over_time(go_goroutine_count{{{t}}}[1m])'),
        "transfers CPU": prom(f'sum(rate(process_cpu_time_seconds_total{{{t}}}[1m]))'),
    }
    fmt = lambda v: "–" if v is None else (f"{v:.2f}" if isinstance(v, float) else v)
    say("  ".join(f"{k}={fmt(v)}" for k, v in s.items()))
    return s


QUERIES = {
    "p99 latency (ms)": 'histogram_quantile(0.99, sum by (le) (rate(http_server_request_duration_seconds_bucket{job="transfers"}[30s]))) * 1000',
    "p50 latency (ms)": 'histogram_quantile(0.5, sum by (le) (rate(http_server_request_duration_seconds_bucket{job="transfers"}[30s]))) * 1000',
    "requests / s": 'sum(rate(http_server_request_duration_seconds_count{job="transfers"}[30s]))',
    "5xx %": '100 * (sum(rate(http_server_request_duration_seconds_count{job="transfers",http_response_status_code=~"5.."}[30s])) or vector(0)) / sum(rate(http_server_request_duration_seconds_count{job="transfers"}[30s]))',
    "goroutines": 'max(go_goroutine_count{job="transfers"})',
    "transfers CPU (cores)": 'sum(rate(process_cpu_time_seconds_total{job="transfers"}[30s]))',
}


def chart(minutes: int = 5) -> None:
    """The RED and USE numbers for the last few minutes, drawn in the notebook."""
    import matplotlib.pyplot as plt
    end = time.time()
    fig, axes = plt.subplots(2, 3, figsize=(15, 6), sharex=True)
    for ax, (title, q) in zip(axes.flat, QUERIES.items()):
        r = get_json(f"{PROM}/api/v1/query_range", {"query": q, "start": end - minutes * 60, "end": end, "step": 5})
        for series in r["data"]["result"]:
            xs = [(float(t) - end) / 60 for t, _ in series["values"]]
            ys = [float(v) if v not in ("NaN", "+Inf") else float("nan") for _, v in series["values"]]
            ax.plot(xs, ys, color="#c2412d" if "p99" in title else "#0c6a87")
        if "p99" in title:
            ax.axhline(300, color="#a8670f", linestyle="--", linewidth=1, label="SLO 300 ms")
            ax.legend(loc="upper left")
        ax.set_title(title)
        ax.grid(alpha=0.3)
    for ax in axes[1]:
        ax.set_xlabel("minutes ago")
    fig.tight_layout()
    plt.show()


def _b64hex(s: str) -> str:
    import base64
    return base64.b64decode(s).hex() if s else ""


def _attr(v: dict):
    return v.get("stringValue", v.get("intValue", v.get("doubleValue", v.get("boolValue", ""))))


def _changed() -> None:
    """Remember when the system last changed (stage, bug, surge), so trace()
    only looks at requests that happened after it."""
    _state["since"] = time.time()
    _save_state()


def traces(minutes: float = 2, limit: int = 20, since: float | None = None) -> list[dict]:
    """Transfer traces from Tempo that started after `since` (default: the last
    change), slowest first."""
    # Tempo returns the most recent matches, not the slowest, so ask for slow
    # ones first: root spans over 1 s, then 300 ms, then 100 ms, then any.
    end = int(time.time())
    since = since if since is not None else max(_state.get("since", 0), end - minutes * 60)
    for floor in ["1s", "300ms", "100ms", None]:
        q = '{resource.service.name="transfers" && name="POST /transfers"' + (f" && duration > {floor}" if floor else "") + "}"
        # Tempo needs a window of at least a minute; filter by start time after.
        r = get_json(f"{TEMPO}/api/search", {"q": q, "limit": limit, "start": int(min(since, end - 60)), "end": end})
        out = [{"traceID": t["traceID"].zfill(32), "ms": t.get("durationMs", 0), "root": t.get("rootTraceName")}
               for t in r.get("traces", []) if int(t.get("startTimeUnixNano", 0)) / 1e9 >= since]
        if out:
            return sorted(out, key=lambda t: -t["ms"])
    return []


def trace(trace_id: str | None = None, minutes: float = 2, draw: bool = True, wait: int = 45) -> dict:
    """Fetch one trace (default: the slowest since the last change) and draw its waterfall.

    A finished trace takes a few seconds to become searchable in Tempo, so
    when a bug is on and nothing slow has arrived yet, this waits up to
    `wait` seconds for one."""
    if trace_id is None:
        t0 = time.time()
        bug_on = any(v is True for k, v in _bugs_state().items() if k in "abc")
        while True:
            found = traces(minutes)
            slow = found and found[0]["ms"] >= 300
            if slow or not bug_on or time.time() - t0 > wait:
                break
            if time.time() - t0 < 1:
                say("waiting for slow traces to reach Tempo…")
            time.sleep(5)
        if not found:
            raise RuntimeError("no traces yet: is traffic running (w.load()) and are you on 02-traces or later?")
        trace_id = found[0]["traceID"]
    res = get_json(f"{TEMPO}/api/traces/{trace_id}")
    spans = []
    for batch in res.get("batches") or res.get("resourceSpans") or []:
        svc = next((_attr(a["value"]) for a in batch["resource"].get("attributes", []) if a["key"] == "service.name"), "")
        for ss in batch.get("scopeSpans", []):
            for s in ss.get("spans", []):
                st, en = int(s["startTimeUnixNano"]), int(s["endTimeUnixNano"])
                spans.append({"id": _b64hex(s["spanId"]), "parent": _b64hex(s.get("parentSpanId", "")),
                              "name": s["name"], "service": svc, "start": st, "ms": (en - st) / 1e6,
                              "attrs": {a["key"]: _attr(a["value"]) for a in s.get("attributes", [])}})
    by_id = {s["id"]: s for s in spans}
    for s in spans:
        s["child_ms"] = 0.0
    for s in spans:
        if s["parent"] in by_id:
            by_id[s["parent"]]["child_ms"] += s["ms"]
    for s in spans:
        s["self_ms"] = max(0.0, s["ms"] - s["child_ms"])
    spans.sort(key=lambda s: s["start"])
    widest = max(spans, key=lambda s: s["self_ms"])
    t0 = spans[0]["start"]
    total = max((s["start"] + s["ms"] * 1e6 for s in spans)) - t0
    say(f"trace {trace_id}: {total / 1e6:.1f} ms, {len(spans)} spans, services {sorted({s['service'] for s in spans})}")
    say(f"most time of its own: '{widest['name']}' in {widest['service']}, {widest['self_ms']:.1f} ms")
    _trace_link(trace_id)
    if draw:
        import matplotlib.pyplot as plt
        depth = {}
        for s in spans:
            depth[s["id"]] = depth.get(s["parent"], -1) + 1
        fig, ax = plt.subplots(figsize=(13, 0.35 * len(spans) + 1))
        for i, s in enumerate(spans):
            left = (s["start"] - t0) / 1e6
            color = "#c2412d" if s is widest else {"transfers": "#0c6a87", "ledger": "#2c7d4a", "provider": "#7a4fa3"}.get(s["service"], "#888")
            ax.barh(i, max(s["ms"], total / 1e6 / 400), left=left, color=color)
            ax.text(left + s["ms"] + total / 1e6 / 150, i, f"{s['ms']:.1f} ms", va="center", fontsize=8)
        ax.set_yticks(range(len(spans)))
        ax.set_yticklabels([("  " * depth[s["id"]]) + f"{s['service']}: {s['name']}" for s in spans], fontsize=9)
        ax.invert_yaxis()
        ax.set_xlabel("ms since the request started")
        ax.set_title("red = the span with the most time of its own")
        ax.grid(axis="x", alpha=0.3)
        fig.tight_layout()
        plt.show()
    return {"traceID": trace_id, "widest": widest["name"], "spans": spans}


def _trace_link(trace_id: str) -> None:
    """A link that opens this exact trace in Grafana's Explore."""
    panes = {"a": {"datasource": "tempo", "range": {"from": "now-30m", "to": "now"},
                   "queries": [{"refId": "A", "datasource": {"type": "tempo", "uid": "tempo"},
                                "queryType": "traceql", "limit": 20, "query": trace_id}]}}
    url = (_state.get("grafana_url", GRAFANA + "/").rstrip("/") + "/explore?schemaVersion=1&panes="
           + urllib.parse.quote(json.dumps(panes, separators=(",", ":"))))
    try:
        from IPython.display import HTML, display
        display(HTML(f'<p>Open this trace in Grafana: <a href="{url}" target="_blank">{trace_id}</a></p>'))
    except ImportError:
        say(f"Grafana: {url}")


def logs(trace_id: str, minutes: int = 10) -> list[dict]:
    """Every log line in Loki for one trace, with its fields (Related logs, in the notebook)."""
    end = time.time_ns()
    r = get_json(f"{LOKI}/loki/api/v1/query_range", {
        "query": f'{{service_name=~"transfers|ledger|provider"}} | trace_id="{trace_id}"',
        "start": end - minutes * 60 * 10**9, "end": end, "limit": 100})
    noise = {"service_name", "observed_timestamp", "flags", "scope_name", "telemetry_sdk_language",
             "telemetry_sdk_name", "telemetry_sdk_version", "severity_number", "deployment_environment_name",
             "span_id", "trace_id", "service_instance_id"}
    out = []
    for stream in r["data"]["result"]:
        fields = {k: v for k, v in stream["stream"].items() if k not in noise}
        for _, line in stream["values"]:
            out.append({"service": stream["stream"].get("service_name"), "level": fields.pop("detected_level", fields.get("severity_text", "")), "line": line, **fields})
    if not out:
        say("no log lines for this trace (logs arrive on 03-logs and later)")
    for o in out:
        extra = "  ".join(f"{k}={v}" for k, v in o.items() if k not in {"service", "level", "line", "severity_text"})
        say(f"[{o['service']}] {str(o['level']).upper():5} {o['line']}\n        {extra}")
    return out


def grafana(path: str = "d/payments") -> None:
    """A link to Grafana (it opens in a new tab through Colab's proxy)."""
    url = _state.get("grafana_url", GRAFANA + "/").rstrip("/") + "/" + path.lstrip("/")
    try:
        from IPython.display import HTML, display
        display(HTML(f'<p style="font-size:16px">Grafana: <a href="{url}" target="_blank">{url}</a> '
                     f'(opens in a new tab; no login)</p>'))
    except ImportError:
        say(f"Grafana: {url}")


# --- alerts -----------------------------------------------------------------

def subscribe() -> None:
    """Your own ntfy topic, as a link and a QR code (make subscribe)."""
    topic = _state["ntfy_topic"]
    link = f"https://ntfy.sh/{topic}"
    say(f"1. Install the ntfy app (App Store or Google Play)\n2. Tap + and subscribe to:\n\n      {topic}\n\n   or open {link}")
    qr = BIN / _state.get("stage", "main") / "qr"
    if qr.exists():
        png = ROOT / "subscribe-qr.png"
        sh(f"{qr} {link} {png} >/dev/null")
        try:
            from IPython.display import Image, display
            display(Image(filename=str(png), width=260))
        except ImportError:
            pass


def alert_test() -> None:
    """Send a sample alert through the relay to ntfy (make alert-test)."""
    if not (BIN / _state.get("stage", "main") / "alertrelay").exists():
        raise RuntimeError('alerting arrives on 04-alerts: w.stage("04-alerts")')
    relay = "http://127.0.0.1:%d/test" % P["alertrelay"]
    say("sent: " + _post(relay))
    say(f"phones subscribed to https://ntfy.sh/{_state['ntfy_topic']} should buzz now")


def alerts() -> None:
    """The state of each alert rule in Grafana: Normal, Pending or Firing."""
    status, body = http("GET", f"{GRAFANA}/api/prometheus/grafana/api/v1/rules",
                        headers={"Authorization": "Basic YWRtaW46YWRtaW4="})
    for g in json.loads(body).get("data", {}).get("groups", []):
        for r in g.get("rules", []):
            say(f"{r.get('state', '?'):8} {r.get('name')}")


def wait_for_alert(timeout: int = 120) -> None:
    """Watch the p99 rule until it fires, printing what it is doing."""
    t0 = time.time()
    last = None
    while time.time() - t0 < timeout:
        status, body = http("GET", f"{GRAFANA}/api/prometheus/grafana/api/v1/rules",
                            headers={"Authorization": "Basic YWRtaW46YWRtaW4="})
        state = "?"
        for g in json.loads(body).get("data", {}).get("groups", []):
            for r in g.get("rules", []):
                if "p99" in r.get("name", ""):
                    state = r.get("state")
        if state != last:
            say(f"{time.time() - t0:5.0f}s  p99 rule: {state}")
            last = state
        if state == "firing":
            say("check your phone")
            return
        time.sleep(3)
    say("did not fire yet; is traffic running and is bug C on?")


# --- the hunt ----------------------------------------------------------------

HINTS = [
    "Hint 1 (what): which RED number went bad in w.chart()? Which USE number moved with it, and which did not?",
    "Hint 2 (where): w.trace() opens the slowest recent trace. Which span has the most time of its own?",
    "Hint 3 (where): a span that is wide with nothing under it is where the time is really spent.",
    "Hint 4 (why): w.logs(trace_id) shows that trace's log lines. Read the WARN line; every number matters.",
]


def mystery(which: str | None = None) -> None:
    """Turn on one random bug and keep it secret (on the hunt branch)."""
    bugs_off()
    if not running("loadgen"):
        load()
    which = which or random.choice("abc")
    _post(f"{T_ADMIN if which in 'ac' else L_ADMIN}?{which}=on")
    if which == "a":
        surge(SURGE_RATE, 900)  # bug A only shows under traffic
    _state["hunt"] = {"bug": which, "started": time.time(), "hints": 0}
    _changed()
    say("A bug is live in production. Something is wrong, somewhere, for some reason.\n"
        "Find what, where and why using only the signals: w.chart(), w.trace(), w.logs(), Grafana.")


def hint() -> None:
    h = _state.get("hunt")
    if not h:
        raise RuntimeError("no hunt running: w.mystery()")
    say(HINTS[min(h["hints"], len(HINTS) - 1)])
    h["hints"] += 1
    _save_state()


def reveal() -> None:
    h = _state.pop("hunt", None)
    if not h:
        raise RuntimeError("no hunt running: w.mystery()")
    say(f"Hunted for {(time.time() - h['started']) / 60:.0f} min.")
    say({"a": "Bug A: the duplicate check walks every transfer of the last 24 h, lower-casing each key, under one lock.",
         "b": "Bug B: the index on entries(account_id) is gone; every balance check is a sequential scan (w.explain()).",
         "c": "Bug C: the account lock is held across the provider call; every payout from the wallet queues behind the last."}[h["bug"]])
    bugs_off()
    _save_state()


def status() -> None:
    """What is running."""
    _load_state()
    names = ["postgres", "otelcol", "prometheus", "tempo", "loki", "grafana", "ledger", "provider", "transfers",
             "alertrelay", "loadgen", "surge"]
    pg = "online" in sh("pg_lsclusters 2>/dev/null", check=False)
    say(f"branch: {_state.get('stage')}   built: {[b for b in BRANCHES if _built[b].is_set() or (BIN / b / '.rev').exists()]}")
    say("  ".join(f"{'✓' if (pg if n == 'postgres' else running(n)) else '·'} {n}" for n in names))


_load_state()
