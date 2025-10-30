#!/usr/bin/env python3
"""
Generate a human-readable Markdown traffic report from GitHub Traffic API snapshots.

This script reads the latest JSON snapshot from metrics/traffic/, computes KPIs,
detects spikes, and generates metrics/traffic/REPORT.md.

Usage:
    python3 scripts/traffic_report.py
"""

import json
import os
import glob
from collections import defaultdict

SNAPSHOT_DIR = os.path.join("metrics", "traffic")
REPORT_PATH = os.path.join(SNAPSHOT_DIR, "REPORT.md")


def latest_snapshot(path=SNAPSHOT_DIR):
    """Find the most recent snapshot file by collected_at timestamp."""
    files = sorted(glob.glob(os.path.join(path, "*.json")))
    if not files:
        raise SystemExit("No snapshots found in metrics/traffic/")
    
    # Choose newest by collected_at if present, else by filename
    def key(f):
        try:
            with open(f, "r") as fh:
                d = json.load(fh)
            return d.get("collected_at", "")
        except Exception:
            return ""
    
    files.sort(key=key)
    return files[-1]


def load_data():
    """Load the latest snapshot data."""
    f = latest_snapshot()
    with open(f, "r") as fh:
        d = json.load(fh)
    return f, d


def to_date(ts: str) -> str:
    """Extract date from timestamp like 2025-10-25T00:00:00Z -> 2025-10-25."""
    return ts.split("T")[0]


def compute(d):
    """Compute KPIs and detect spikes from the snapshot data."""
    views = d.get("views", {})
    clones = d.get("clones", {})
    referrers = d.get("referrers", [])
    paths = d.get("paths", [])

    v_total = views.get("count", 0)
    v_unique = views.get("uniques", 0)
    c_total = clones.get("count", 0)
    c_unique = clones.get("uniques", 0)

    # Build daily series
    series = defaultdict(lambda: {"views": 0, "v_uniques": 0, "clones": 0, "c_uniques": 0})
    for p in views.get("views", []) or []:
        day = to_date(p.get("timestamp", ""))
        series[day]["views"] = p.get("count", 0)
        series[day]["v_uniques"] = p.get("uniques", 0)
    for p in clones.get("clones", []) or []:
        day = to_date(p.get("timestamp", ""))
        series[day]["clones"] = p.get("count", 0)
        series[day]["c_uniques"] = p.get("uniques", 0)

    days = sorted(series.keys())

    # KPIs
    views_per_visitor = (v_total / v_unique) if v_unique else 0
    clone_to_view = (c_total / v_total) if v_total else 0
    unique_cloner_to_visitor = (c_unique / v_unique) if v_unique else 0
    daily_avg_views = (sum(series[d]["views"] for d in days) / len(days)) if days else 0
    daily_avg_clones = (sum(series[d]["clones"] for d in days) / len(days)) if days else 0

    # Spike detection
    spikes = []
    for d_ in days:
        v = series[d_]["views"]
        vu = series[d_]["v_uniques"] or 0
        c = series[d_]["clones"]
        cu = series[d_]["c_uniques"] or 0
        flags = []
        if vu and (v / vu) > 5:
            flags.append("views/visitor>5")
        if cu and (c / cu) > 3:
            flags.append("clones/unique_cloner>3")
        if v and (c / v) > 0.20:
            flags.append("clones/views>20%")
        if flags:
            spikes.append({
                "date": d_,
                "flags": ", ".join(flags),
                "views": v,
                "uniques": vu,
                "clones": c,
                "unique_cloners": cu
            })

    return {
        "v_total": v_total,
        "v_unique": v_unique,
        "c_total": c_total,
        "c_unique": c_unique,
        "views_per_visitor": views_per_visitor,
        "clone_to_view": clone_to_view,
        "unique_cloner_to_visitor": unique_cloner_to_visitor,
        "daily_avg_views": daily_avg_views,
        "daily_avg_clones": daily_avg_clones,
        "days": days,
        "series": series,
        "spikes": spikes,
        "referrers": referrers,
        "paths": paths,
        "collected_at": d.get("collected_at"),
        "repo": d.get("repo", {})
    }


def render_md(stats) -> str:
    """Render the stats into a Markdown report."""
    owner = stats["repo"].get("owner", "?")
    repo = stats["repo"].get("repo", "?")
    lines = []
    lines.append(f"# Traffic Report for {owner}/{repo}")
    lines.append("")
    lines.append(f"Collected at: {stats['collected_at']}")
    lines.append("")
    lines.append("## Summary (last 14 days)")
    lines.append("")
    lines.append(f"- Views: {stats['v_total']}")
    lines.append(f"- Unique visitors: {stats['v_unique']}")
    lines.append(f"- Clones: {stats['c_total']}")
    lines.append(f"- Unique cloners: {stats['c_unique']}")
    lines.append("")
    lines.append("## Key ratios and averages")
    lines.append("")
    lines.append(f"- Views per unique visitor: {stats['views_per_visitor']:.2f}")
    lines.append(f"- Clone-to-view conversion: {stats['clone_to_view']*100:.1f}%")
    lines.append(f"- Unique cloner-to-unique visitor: {stats['unique_cloner_to_visitor']*100:.1f}%")
    lines.append(f"- Daily average views: {stats['daily_avg_views']:.2f}")
    lines.append(f"- Daily average clones: {stats['daily_avg_clones']:.2f}")
    lines.append("")
    lines.append("## Spike flags")
    lines.append("")
    if stats["spikes"]:
        for s in stats["spikes"]:
            lines.append(f"- {s['date']}: {s['flags']} (views={s['views']}, uniques={s['uniques']}, clones={s['clones']}, unique_cloners={s['unique_cloners']})")
    else:
        lines.append("- None detected by current thresholds")
    lines.append("")
    lines.append("## Daily breakdown")
    lines.append("")
    lines.append("| Date | Views | Unique visitors | Clones | Unique cloners | Views/Visitor | Clones/Unique Cloner | Clones/Views |")
    lines.append("|------|------:|-----------------:|-------:|---------------:|--------------:|----------------------:|-------------:|")
    for d_ in stats["days"]:
        v = stats["series"][d_]["views"]
        vu = stats["series"][d_]["v_uniques"] or 0
        c = stats["series"][d_]["clones"]
        cu = stats["series"][d_]["c_uniques"] or 0
        vpv = (v / vu) if vu else 0
        cpu = (c / cu) if cu else 0
        cv = (c / v) if v else 0
        lines.append(f"| {d_} | {v} | {vu} | {c} | {cu} | {vpv:.2f} | {cpu:.2f} | {cv:.2f} |")
    lines.append("")
    lines.append("## Notes")
    lines.append("")
    lines.append("- Referrers and popular paths may lag up to ~24 hours in GitHub aggregation and can be empty. If they remain empty while page views are high, traffic may be automation-heavy (link unfurlers, crawlers, CI clones).")
    lines.append("- Thresholds for spike flags are heuristics; tune them in scripts/traffic_report.py as needed.")
    lines.append("- Data source: GitHub Traffic API snapshots in metrics/traffic/.")
    return "\n".join(lines)


def main():
    """Main entry point."""
    _, d = load_data()
    stats = compute(d)
    md = render_md(stats)
    os.makedirs(SNAPSHOT_DIR, exist_ok=True)
    with open(REPORT_PATH, "w", encoding="utf-8") as fh:
        fh.write(md)
    print(f"Wrote report to {REPORT_PATH}")


if __name__ == "__main__":
    main()
