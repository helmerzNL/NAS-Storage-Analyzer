#!/usr/bin/env python3
"""Validate this app's app-engineering conformance declaration.

Flux76's baseline (project-baseline.md section 18) makes every app answer 111 engineering
topics, so that "is it secure and fast enough?" has something to be compared against. A topic
left blank cannot be told apart from a topic nobody checked.

This script checks the *declaration*, not the app: it cannot tell whether the evidence is
true, only that the question was answered. It fails on

  * a topic that is missing, renamed or still `unset`;
  * a status other than applies, partial, not-now or n/a;
  * a missing evidence or reason, or a `partial` that names no `Gap:`;
  * an empty cell in the measured-gates table (a target that cannot be measured is not one).

The topic list below is a copy of the index in App-Guidance
(shared/framework/app-engineering/README.md). When App-Guidance adds a topic, add it here and
in the declaration in the same commit. Run by the `app-engineering-conformance` CI job and the
pre-commit hook.
"""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DECLARATION = ROOT / "docs" / "app-engineering-conformance.md"

TOPICS = (
    "Rate limiting",
    "Caching",
    "Load balancing",
    "Reverse proxies",
    "API gateways",
    "CI/CD",
    "Docker",
    "Kubernetes",
    "Service discovery",
    "Circuit breakers",
    "Timeouts",
    "Retries",
    "Exponential backoff",
    "Idempotency",
    "Message queues",
    "Pub/Sub",
    "Event-driven architecture",
    "Distributed transactions",
    "Saga pattern",
    "Dead letter queues",
    "Cron jobs",
    "WebSockets",
    "Long polling",
    "Server-Sent Events",
    "Database indexing",
    "Query optimization",
    "N+1 queries",
    "Connection pooling",
    "Read replicas",
    "Sharding",
    "Partitioning",
    "Replication",
    "Leader election",
    "CAP theorem",
    "Eventual consistency",
    "Optimistic locking",
    "Pessimistic locking",
    "Distributed locks",
    "Race conditions",
    "Deadlocks",
    "Memory leaks",
    "Garbage collection",
    "Thread safety",
    "Backpressure",
    "Autoscaling",
    "Horizontal scaling",
    "Vertical scaling",
    "CDN",
    "Edge caching",
    "Cache invalidation",
    "Feature flags",
    "Blue-green deployment",
    "Canary releases",
    "Rolling deployments",
    "Rollbacks",
    "Health checks",
    "Liveness and readiness probes",
    "Monitoring",
    "Logging",
    "Distributed tracing",
    "Metrics",
    "Alerts",
    "SLOs",
    "SLIs",
    "Error budgets",
    "Observability",
    "Secrets management",
    "IAM",
    "OAuth",
    "JWT rotation",
    "TLS",
    "Encryption at rest",
    "Encryption in transit",
    "WAF",
    "DDoS protection",
    "CORS",
    "CSRF",
    "SQL injection",
    "XSS",
    "SSRF",
    "Database migrations",
    "Schema versioning",
    "Disaster recovery",
    "Backups",
    "Failover",
    "Multi-region deployment",
    "Chaos engineering",
    "Cost optimization",
    "Cold starts",
    "Serverless limits",
    "Latency",
    "Throughput",
    "P99 latency",
    "Tail latency",
    "Network partitions",
    "Clock skew",
    "DNS",
    "TCP vs UDP",
    "HTTP/2 and HTTP/3",
    "gRPC",
    "Webhooks",
    "API versioning",
    "Semantic versioning",
    "Infrastructure as Code",
    "Terraform",
    "Helm charts",
    "Build caching",
    "Dependency hell",
    "Production incidents",
    "On-call",
    "Postmortems",
)

STATUSES = ("applies", "partial", "not-now", "n/a")
GAP_MARKER = "gap:"
UNSET = "unset"
# Text that looks filled in but says nothing.
PLACEHOLDERS = {"", "-", "\u2014", "tbd", "todo", "?", "..."}

ANSWER_ROW_RE = re.compile(
    r"^\|\s*(\d+)\s*\|\s*([^|]+?)\s*\|\s*([^|]*?)\s*\|\s*([^|]*?)\s*\|\s*([^|]*?)\s*\|\s*$"
)
GATE_ROW_RE = re.compile(r"^\|\s*(Performance|Reliability|Security|Operations)\s*\|([^|]*)\|([^|]*)\|([^|]*)\|\s*$")


def read_answers(text: str) -> dict:
    """Topic number -> (name, status, evidence, control)."""
    rows = {}
    for line in text.splitlines():
        m = ANSWER_ROW_RE.match(line)
        if m and m.group(3).strip("` ").lower() in (*STATUSES, UNSET):
            rows[int(m.group(1))] = (m.group(2), m.group(3).strip("` ").lower(), m.group(4), m.group(5))
    return rows


def check_topics(text: str, label: str) -> list:
    answers = read_answers(text)
    errors = []
    for number, name in enumerate(TOPICS, start=1):
        row = answers.get(number)
        if row is None:
            errors.append(f"{label}: topic {number} ({name}) is not answered")
            continue
        found, status, evidence, _control = row
        if found != name:
            errors.append(f"{label}: topic {number} is '{found}', expected '{name}'")
        if status == UNSET:
            errors.append(f"{label}: topic {number} ({name}) is still '{UNSET}'")
        elif evidence.strip().lower() in PLACEHOLDERS:
            what = "evidence" if status in ("applies", "partial") else "a reason"
            errors.append(f"{label}: topic {number} ({name}) is '{status}' without {what}")
        elif status == "partial" and GAP_MARKER not in evidence.lower():
            errors.append(f"{label}: topic {number} ({name}) is 'partial' but names no 'Gap:' (say what is missing, or use 'applies')")
    extra = sorted(set(answers) - set(range(1, len(TOPICS) + 1)))
    if extra:
        errors.append(f"{label}: unknown topic number(s) {extra}")
    return errors


def check_gates(text: str, label: str) -> list:
    """Every measured-gates row has a target and a way of measuring it (\"not measured\" is an answer, blank is not)."""
    errors = []
    rows = 0
    for line in text.splitlines():
        m = GATE_ROW_RE.match(line)
        if not m:
            continue
        rows += 1
        gate, target, measured = (m.group(i).strip() for i in (2, 3, 4))
        if target.lower() in PLACEHOLDERS or measured.lower() in PLACEHOLDERS:
            errors.append(f"{label}: measured gate '{gate}' has an empty target or an empty 'Measured by' (write 'not measured' and why)")
    if rows == 0:
        errors.append(f"{label}: no measured-gates table found (rows starting with Performance, Reliability, Security or Operations)")
    return errors


def main(argv: list) -> int:
    path = Path(argv[0]).resolve() if argv else DECLARATION
    if not path.is_file():
        print(f"App-engineering conformance failed:\n  - {path}: not found", file=sys.stderr)
        return 1
    text = path.read_text(encoding="utf-8")
    label = path.relative_to(ROOT).as_posix() if path.is_relative_to(ROOT) else str(path)
    errors = check_topics(text, label) + check_gates(text, label)
    if errors:
        print("App-engineering conformance failed:", file=sys.stderr)
        for e in errors:
            print(f"  - {e}", file=sys.stderr)
        return 1
    print(f"App-engineering conformance passed: {len(TOPICS)} topics answered in {label}.")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
