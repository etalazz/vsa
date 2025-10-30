#!/usr/bin/env python3
"""
Summarize GitHub Traffic Metrics

This script analyzes the most recent traffic snapshot from metrics/traffic/
and displays summary statistics including totals, daily averages, and top
referrers and paths.

Usage:
    python scripts/summarize_traffic.py [--file YYYY-MM-DD.json]
"""

import json
import os
import sys
from datetime import datetime
from pathlib import Path
from typing import Dict, Any, Optional


def find_latest_snapshot(metrics_dir: Path) -> Optional[Path]:
    """Find the most recent traffic snapshot file."""
    json_files = sorted(metrics_dir.glob("*.json"), reverse=True)
    return json_files[0] if json_files else None


def load_snapshot(filepath: Path) -> Dict[str, Any]:
    """Load a traffic snapshot from a JSON file."""
    with open(filepath, 'r') as f:
        return json.load(f)


def calculate_daily_average(items: list, field: str) -> float:
    """Calculate daily average for a given field."""
    if not items:
        return 0.0
    return sum(item.get(field, 0) for item in items) / len(items)


def summarize_traffic(data: Dict[str, Any]) -> None:
    """Print a summary of the traffic data."""
    print("=" * 70)
    print("GitHub Traffic Metrics Summary")
    print("=" * 70)
    print()
    
    # Repository and timestamp
    print(f"Repository: {data.get('repository', 'N/A')}")
    print(f"Snapshot timestamp: {data.get('timestamp', 'N/A')}")
    print()
    
    # Views summary
    views = data.get('views', {})
    print("VIEWS")
    print("-" * 70)
    print(f"  Total count: {views.get('count', 0):,}")
    print(f"  Total uniques: {views.get('uniques', 0):,}")
    
    views_data = views.get('views', [])
    if views_data:
        avg_count = calculate_daily_average(views_data, 'count')
        avg_uniques = calculate_daily_average(views_data, 'uniques')
        print(f"  Daily average count: {avg_count:.1f}")
        print(f"  Daily average uniques: {avg_uniques:.1f}")
        print(f"  Days tracked: {len(views_data)}")
    print()
    
    # Clones summary
    clones = data.get('clones', {})
    print("CLONES")
    print("-" * 70)
    print(f"  Total count: {clones.get('count', 0):,}")
    print(f"  Total uniques: {clones.get('uniques', 0):,}")
    
    clones_data = clones.get('clones', [])
    if clones_data:
        avg_count = calculate_daily_average(clones_data, 'count')
        avg_uniques = calculate_daily_average(clones_data, 'uniques')
        print(f"  Daily average count: {avg_count:.1f}")
        print(f"  Daily average uniques: {avg_uniques:.1f}")
        print(f"  Days tracked: {len(clones_data)}")
    print()
    
    # Top referrers
    referrers = data.get('referrers', [])
    print(f"TOP REFERRERS ({len(referrers)} total)")
    print("-" * 70)
    if referrers:
        for i, ref in enumerate(referrers[:10], 1):
            print(f"  {i:2d}. {ref.get('referrer', 'N/A'):40s} "
                  f"count: {ref.get('count', 0):5,}  uniques: {ref.get('uniques', 0):5,}")
    else:
        print("  No referrer data available")
    print()
    
    # Top paths
    paths = data.get('paths', [])
    print(f"TOP PATHS ({len(paths)} total)")
    print("-" * 70)
    if paths:
        for i, p in enumerate(paths[:10], 1):
            print(f"  {i:2d}. {p.get('path', 'N/A'):40s} "
                  f"count: {p.get('count', 0):5,}  uniques: {p.get('uniques', 0):5,}")
    else:
        print("  No path data available")
    print()
    
    print("=" * 70)


def main():
    """Main entry point."""
    # Determine the metrics directory
    script_dir = Path(__file__).parent
    repo_root = script_dir.parent
    metrics_dir = repo_root / "metrics" / "traffic"
    
    # Check if a specific file was provided
    if len(sys.argv) > 1 and sys.argv[1] not in ['-h', '--help']:
        if sys.argv[1] == '--file' and len(sys.argv) > 2:
            filepath = metrics_dir / sys.argv[2]
        else:
            filepath = Path(sys.argv[1])
        
        if not filepath.exists():
            print(f"Error: File not found: {filepath}", file=sys.stderr)
            sys.exit(1)
    else:
        if sys.argv[1:2] == ['-h'] or sys.argv[1:2] == ['--help']:
            print(__doc__)
            sys.exit(0)
        
        # Find the latest snapshot
        if not metrics_dir.exists():
            print(f"Error: Metrics directory not found: {metrics_dir}", file=sys.stderr)
            sys.exit(1)
        
        filepath = find_latest_snapshot(metrics_dir)
        if not filepath:
            print(f"Error: No traffic snapshots found in {metrics_dir}", file=sys.stderr)
            sys.exit(1)
    
    # Load and summarize the data
    print(f"Reading snapshot: {filepath.name}\n")
    data = load_snapshot(filepath)
    summarize_traffic(data)


if __name__ == "__main__":
    main()
