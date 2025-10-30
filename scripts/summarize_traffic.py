#!/usr/bin/env python3
"""
Summarize GitHub traffic metrics from the most recent snapshot.

This script reads the latest JSON snapshot from metrics/traffic/ and displays:
- Total views and unique visitors
- Total clones and unique cloners
- Daily averages
- Top referrers
- Popular paths

Usage:
    python3 summarize_traffic.py [metrics_dir]

Arguments:
    metrics_dir    Optional path to metrics directory (default: ../metrics from script location)
"""

import json
import os
import sys
from datetime import datetime
from pathlib import Path


def find_latest_snapshot(metrics_dir):
    """Find the most recent traffic metrics snapshot."""
    traffic_dir = Path(metrics_dir) / "traffic"
    
    if not traffic_dir.exists():
        print(f"Error: Traffic metrics directory not found: {traffic_dir}")
        sys.exit(1)
    
    # Find all JSON files (excluding .gitkeep)
    json_files = list(traffic_dir.glob("*.json"))
    
    if not json_files:
        print(f"Error: No traffic snapshot files found in {traffic_dir}")
        sys.exit(1)
    
    # Sort by filename (which is YYYY-MM-DD.json) to get the latest
    latest_file = sorted(json_files)[-1]
    return latest_file


def format_number(num):
    """Format number with commas for readability."""
    return f"{num:,}"


def summarize_metrics(snapshot_path):
    """Load and summarize traffic metrics from a snapshot file."""
    with open(snapshot_path, 'r') as f:
        data = json.load(f)
    
    print("=" * 70)
    print(f"GitHub Traffic Metrics Summary")
    print("=" * 70)
    print(f"Repository: {data.get('repository', 'N/A')}")
    print(f"Snapshot Date: {data.get('date', 'N/A')}")
    print(f"Collected At: {data.get('timestamp', 'N/A')}")
    print("=" * 70)
    
    # Views summary
    views = data.get('views')
    if views:
        total_views = views.get('count', 0)
        unique_views = views.get('uniques', 0)
        views_data = views.get('views', [])
        num_days = len(views_data)
        
        print("\n📊 VIEWS")
        print(f"  Total Views: {format_number(total_views)}")
        print(f"  Unique Visitors: {format_number(unique_views)}")
        if num_days > 0:
            avg_daily_views = total_views / num_days
            avg_daily_uniques = unique_views / num_days
            print(f"  Daily Average: {avg_daily_views:.1f} views, {avg_daily_uniques:.1f} unique")
            print(f"  Period: {num_days} days")
    else:
        print("\n📊 VIEWS: No data available")
    
    # Clones summary
    clones = data.get('clones')
    if clones:
        total_clones = clones.get('count', 0)
        unique_clones = clones.get('uniques', 0)
        clones_data = clones.get('clones', [])
        num_days = len(clones_data)
        
        print("\n📦 CLONES")
        print(f"  Total Clones: {format_number(total_clones)}")
        print(f"  Unique Cloners: {format_number(unique_clones)}")
        if num_days > 0:
            avg_daily_clones = total_clones / num_days
            avg_daily_unique_clones = unique_clones / num_days
            print(f"  Daily Average: {avg_daily_clones:.1f} clones, {avg_daily_unique_clones:.1f} unique")
            print(f"  Period: {num_days} days")
    else:
        print("\n📦 CLONES: No data available")
    
    # Top referrers
    referrers = data.get('referrers', [])
    if referrers:
        print("\n🔗 TOP REFERRERS")
        for i, ref in enumerate(referrers[:10], 1):
            referrer = ref.get('referrer', 'Direct/Unknown')
            count = ref.get('count', 0)
            uniques = ref.get('uniques', 0)
            print(f"  {i:2d}. {referrer:40s} {format_number(count):>8s} views, {format_number(uniques):>6s} unique")
    else:
        print("\n🔗 TOP REFERRERS: No data available")
    
    # Popular paths
    paths = data.get('paths', [])
    if paths:
        print("\n📄 POPULAR PATHS")
        for i, path_data in enumerate(paths[:10], 1):
            path = path_data.get('path', '/')
            count = path_data.get('count', 0)
            uniques = path_data.get('uniques', 0)
            title = path_data.get('title', '')
            display_path = f"{path} ({title})" if title else path
            print(f"  {i:2d}. {display_path:60s} {format_number(count):>8s} views, {format_number(uniques):>6s} unique")
    else:
        print("\n📄 POPULAR PATHS: No data available")
    
    print("\n" + "=" * 70)


def main():
    """Main function to run the summary."""
    # Check for help flag
    if len(sys.argv) > 1 and sys.argv[1] in ['-h', '--help', 'help']:
        print(__doc__)
        sys.exit(0)
    
    # Determine the metrics directory
    # Default to ../metrics from script location, or use provided argument
    if len(sys.argv) > 1:
        metrics_dir = sys.argv[1]
    else:
        # Assume script is in scripts/ and metrics/ is at repo root
        script_dir = Path(__file__).parent
        metrics_dir = script_dir.parent / "metrics"
    
    # Find and summarize the latest snapshot
    latest_snapshot = find_latest_snapshot(metrics_dir)
    print(f"Reading snapshot: {latest_snapshot.name}\n")
    summarize_metrics(latest_snapshot)


if __name__ == "__main__":
    main()
