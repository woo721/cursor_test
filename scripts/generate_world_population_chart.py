#!/usr/bin/env python3
"""Generate an offline HTML visualization of country population totals."""

from __future__ import annotations

import argparse
import html
import json
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any


COUNTRIES_URL = "https://api.worldbank.org/v2/country?format=json&per_page=400"
POPULATION_URL = (
    "https://api.worldbank.org/v2/country/all/indicator/SP.POP.TOTL"
    "?format=json&per_page=20000"
)
DEFAULT_OUTPUT = Path("visualizations/world_population_chart.html")
DEFAULT_DATA_OUTPUT = Path("data/world_population_latest.json")


def fetch_world_bank_pages(url: str) -> list[dict[str, Any]]:
    """Fetch every page from a World Bank JSON endpoint."""
    first_page = _fetch_json(url)
    if not isinstance(first_page, list) or len(first_page) != 2:
        raise ValueError("Unexpected World Bank response shape")

    metadata, records = first_page
    pages = int(metadata.get("pages", 1))
    all_records = list(records or [])

    for page in range(2, pages + 1):
        page_url = _with_query_param(url, "page", str(page))
        response = _fetch_json(page_url)
        all_records.extend(response[1] or [])

    return all_records


def load_country_metadata(records: list[dict[str, Any]]) -> dict[str, dict[str, str]]:
    """Return real countries keyed by ISO3 code, excluding World Bank aggregates."""
    countries: dict[str, dict[str, str]] = {}
    for record in records:
        iso3 = str(record.get("id") or "")
        region = record.get("region") or {}
        region_id = region.get("id")
        if not iso3 or region_id == "NA":
            continue

        countries[iso3] = {
            "name": str(record.get("name") or iso3),
            "region": str(region.get("value") or "Unknown"),
        }

    return countries


def latest_country_populations(
    records: list[dict[str, Any]],
    countries: dict[str, dict[str, str]],
) -> list[dict[str, Any]]:
    """Pick the latest non-null population value for each real country."""
    latest: dict[str, dict[str, Any]] = {}
    for record in records:
        iso3 = str(record.get("countryiso3code") or "")
        value = record.get("value")
        if iso3 not in countries or value is None:
            continue

        try:
            year = int(record["date"])
            population = int(round(float(value)))
        except (KeyError, TypeError, ValueError):
            continue

        if iso3 not in latest or year > latest[iso3]["year"]:
            latest[iso3] = {
                "iso3": iso3,
                "country": countries[iso3]["name"],
                "region": countries[iso3]["region"],
                "population": population,
                "year": year,
            }

    return sorted(latest.values(), key=lambda item: item["population"], reverse=True)


def format_population(value: int) -> str:
    """Format population counts with compact human-readable units."""
    if value >= 1_000_000_000:
        return f"{value / 1_000_000_000:.2f}B"
    if value >= 1_000_000:
        return f"{value / 1_000_000:.1f}M"
    if value >= 1_000:
        return f"{value / 1_000:.1f}K"
    return str(value)


def build_chart_html(
    populations: list[dict[str, Any]],
    generated_at: str | None = None,
    top_n: int = 30,
) -> str:
    """Build a self-contained HTML chart for the latest country populations."""
    if not populations:
        raise ValueError("No population data available")

    years = sorted({item["year"] for item in populations})
    generated_at = generated_at or f"latest available data years {years[0]}-{years[-1]}"
    total_population = sum(item["population"] for item in populations)
    svg = _build_svg_bar_chart(populations[:top_n], top_n)
    rows = "\n".join(_table_row(item, index) for index, item in enumerate(populations, start=1))

    return f"""<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>World Country Population Totals</title>
  <style>
    :root {{
      color-scheme: light;
      --bg: #f8fafc;
      --card: #ffffff;
      --ink: #172033;
      --muted: #64748b;
      --accent: #2563eb;
      --accent-soft: #dbeafe;
      --grid: #e2e8f0;
    }}
    body {{
      margin: 0;
      background: var(--bg);
      color: var(--ink);
      font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      line-height: 1.5;
    }}
    main {{
      max-width: 1180px;
      margin: 0 auto;
      padding: 32px 20px 48px;
    }}
    .hero, .card {{
      background: var(--card);
      border: 1px solid var(--grid);
      border-radius: 18px;
      box-shadow: 0 14px 35px rgba(15, 23, 42, 0.08);
    }}
    .hero {{
      padding: 28px;
      margin-bottom: 24px;
    }}
    h1 {{
      margin: 0 0 8px;
      font-size: clamp(2rem, 4vw, 3.5rem);
      line-height: 1.05;
    }}
    h2 {{
      margin: 0 0 16px;
      font-size: 1.35rem;
    }}
    p {{
      margin: 0;
      color: var(--muted);
    }}
    .stats {{
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
      gap: 12px;
      margin-top: 22px;
    }}
    .stat {{
      background: var(--accent-soft);
      border-radius: 14px;
      padding: 16px;
    }}
    .stat strong {{
      display: block;
      color: var(--accent);
      font-size: 1.55rem;
      line-height: 1.1;
    }}
    .card {{
      margin-top: 24px;
      padding: 22px;
      overflow-x: auto;
    }}
    svg {{
      width: 100%;
      height: auto;
      min-width: 880px;
    }}
    table {{
      width: 100%;
      border-collapse: collapse;
      font-size: 0.95rem;
    }}
    th, td {{
      padding: 10px 12px;
      border-bottom: 1px solid var(--grid);
      text-align: left;
      white-space: nowrap;
    }}
    th {{
      color: var(--muted);
      font-size: 0.78rem;
      letter-spacing: 0.06em;
      text-transform: uppercase;
    }}
    td:nth-child(1), td:nth-child(5), td:nth-child(6) {{
      text-align: right;
      font-variant-numeric: tabular-nums;
    }}
    .source {{
      margin-top: 16px;
      font-size: 0.88rem;
    }}
  </style>
</head>
<body>
  <main>
    <section class="hero">
      <h1>World Country Population Totals</h1>
      <p>Latest available World Bank population value for each country. The bar chart shows the top {top_n}; the table includes all countries in the dataset.</p>
      <div class="stats">
        <div class="stat"><strong>{len(populations)}</strong><span>countries</span></div>
        <div class="stat"><strong>{format_population(total_population)}</strong><span>combined population</span></div>
        <div class="stat"><strong>{years[0]}-{years[-1]}</strong><span>latest available years</span></div>
      </div>
      <p class="source">Source: World Bank indicator SP.POP.TOTL. Data snapshot: {html.escape(generated_at)}.</p>
    </section>

    <section class="card">
      <h2>Top {top_n} countries by population</h2>
      {svg}
    </section>

    <section class="card">
      <h2>All countries</h2>
      <table>
        <thead>
          <tr>
            <th>Rank</th>
            <th>Country</th>
            <th>ISO3</th>
            <th>Region</th>
            <th>Year</th>
            <th>Population</th>
          </tr>
        </thead>
        <tbody>
{rows}
        </tbody>
      </table>
    </section>
  </main>
</body>
</html>
"""


def generate_chart(output: Path, data_output: Path, top_n: int) -> None:
    countries = load_country_metadata(fetch_world_bank_pages(COUNTRIES_URL))
    populations = latest_country_populations(fetch_world_bank_pages(POPULATION_URL), countries)

    data_output.parent.mkdir(parents=True, exist_ok=True)
    data_output.write_text(json.dumps(populations, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(build_chart_html(populations, top_n=top_n), encoding="utf-8")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=DEFAULT_OUTPUT)
    parser.add_argument("--data-output", type=Path, default=DEFAULT_DATA_OUTPUT)
    parser.add_argument("--top-n", type=int, default=30)
    args = parser.parse_args()

    generate_chart(args.output, args.data_output, args.top_n)


def _fetch_json(url: str) -> Any:
    request = urllib.request.Request(url, headers={"User-Agent": "cursor-world-population-chart/1.0"})
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.loads(response.read().decode("utf-8"))


def _with_query_param(url: str, key: str, value: str) -> str:
    parsed = urllib.parse.urlparse(url)
    query = dict(urllib.parse.parse_qsl(parsed.query, keep_blank_values=True))
    query[key] = value
    return urllib.parse.urlunparse(parsed._replace(query=urllib.parse.urlencode(query)))


def _build_svg_bar_chart(populations: list[dict[str, Any]], top_n: int) -> str:
    chart_width = 1160
    label_width = 270
    bar_x = 305
    bar_max_width = 710
    value_x = bar_x + bar_max_width + 18
    row_height = 30
    top_padding = 34
    bottom_padding = 24
    chart_height = top_padding + len(populations) * row_height + bottom_padding
    max_population = max(item["population"] for item in populations)

    parts = [
        f'<svg viewBox="0 0 {chart_width} {chart_height}" role="img" aria-label="Top {top_n} countries by population">',
        '<rect width="100%" height="100%" fill="#ffffff"/>',
        f'<text x="{label_width}" y="18" text-anchor="end" fill="#64748b" font-size="12">Country</text>',
        f'<text x="{bar_x}" y="18" fill="#64748b" font-size="12">Population</text>',
    ]

    for index, item in enumerate(populations, start=1):
        y = top_padding + (index - 1) * row_height
        bar_width = max(2, round(item["population"] / max_population * bar_max_width))
        country = html.escape(item["country"])
        value = html.escape(format_population(item["population"]))
        rank = str(index).rjust(2)
        fill = "#2563eb" if index <= 10 else "#60a5fa"

        parts.extend(
            [
                f'<text x="24" y="{y + 17}" fill="#94a3b8" font-size="12">{rank}</text>',
                f'<text x="{label_width}" y="{y + 17}" text-anchor="end" fill="#172033" font-size="13">{country}</text>',
                f'<rect x="{bar_x}" y="{y + 5}" width="{bar_width}" height="18" rx="5" fill="{fill}"/>',
                f'<text x="{value_x}" y="{y + 18}" fill="#172033" font-size="13">{value}</text>',
            ]
        )

    parts.append("</svg>")
    return "\n".join(parts)


def _table_row(item: dict[str, Any], index: int) -> str:
    return (
        "          <tr>"
        f"<td>{index}</td>"
        f"<td>{html.escape(item['country'])}</td>"
        f"<td>{html.escape(item['iso3'])}</td>"
        f"<td>{html.escape(item['region'])}</td>"
        f"<td>{item['year']}</td>"
        f"<td>{item['population']:,}</td>"
        "</tr>"
    )


if __name__ == "__main__":
    main()
