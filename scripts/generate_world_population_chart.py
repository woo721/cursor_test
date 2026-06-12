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


def load_country_metadata(records: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    """Return real countries keyed by ISO3 code, excluding World Bank aggregates."""
    countries: dict[str, dict[str, str]] = {}
    for record in records:
        iso3 = str(record.get("id") or "")
        region = record.get("region") or {}
        region_id = region.get("id")
        if not iso3 or region_id == "NA":
            continue

        country: dict[str, Any] = {
            "name": str(record.get("name") or iso3),
            "region": str(region.get("value") or "Unknown"),
        }
        latitude = _parse_float(record.get("latitude"))
        longitude = _parse_float(record.get("longitude"))
        if latitude is not None and longitude is not None:
            country["latitude"] = latitude
            country["longitude"] = longitude

        countries[iso3] = country

    return countries


def latest_country_populations(
    records: list[dict[str, Any]],
    countries: dict[str, dict[str, Any]],
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
            country = countries[iso3]
            item = {
                "iso3": iso3,
                "country": country["name"],
                "region": country["region"],
                "population": population,
                "year": year,
            }
            if "latitude" in country and "longitude" in country:
                item["latitude"] = country["latitude"]
                item["longitude"] = country["longitude"]
            latest[iso3] = item

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
    region_options = "\n".join(
        f'          <option value="{html.escape(region)}">{html.escape(region)}</option>'
        for region in sorted({item["region"] for item in populations})
    )
    data_json = json.dumps(populations, ensure_ascii=False).replace("</", "<\\/")
    top_count_options = _top_count_options(top_n)

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
    .controls {{
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
      gap: 14px;
      margin-top: 24px;
      padding-top: 22px;
      border-top: 1px solid var(--grid);
    }}
    label {{
      display: grid;
      gap: 6px;
      color: var(--muted);
      font-size: 0.82rem;
      font-weight: 700;
      letter-spacing: 0.04em;
      text-transform: uppercase;
    }}
    input, select {{
      width: 100%;
      box-sizing: border-box;
      border: 1px solid var(--grid);
      border-radius: 12px;
      background: #ffffff;
      color: var(--ink);
      font: inherit;
      padding: 11px 12px;
      outline: none;
    }}
    input:focus, select:focus {{
      border-color: var(--accent);
      box-shadow: 0 0 0 3px rgba(37, 99, 235, 0.16);
    }}
    .chart-grid {{
      display: grid;
      grid-template-columns: minmax(0, 1.4fr) minmax(280px, 0.8fr);
      gap: 24px;
      align-items: stretch;
    }}
    .card {{
      margin-top: 24px;
      padding: 22px;
      overflow-x: auto;
    }}
    .card.full {{
      grid-column: 1 / -1;
    }}
    .chart-note {{
      margin-bottom: 16px;
      font-size: 0.9rem;
    }}
    .legend {{
      display: grid;
      gap: 8px;
      margin-top: 12px;
    }}
    .legend-row {{
      display: grid;
      grid-template-columns: 14px 1fr auto;
      gap: 8px;
      align-items: center;
      color: var(--muted);
      font-size: 0.9rem;
    }}
    .legend-swatch {{
      width: 12px;
      height: 12px;
      border-radius: 999px;
    }}
    .map-frame {{
      position: relative;
      min-width: 780px;
    }}
    .map-frame::before {{
      content: "";
      position: absolute;
      inset: 42px 36px 38px;
      border-radius: 50%;
      background: linear-gradient(135deg, rgba(219, 234, 254, 0.7), rgba(240, 253, 250, 0.7));
      pointer-events: none;
    }}
    svg {{
      width: 100%;
      height: auto;
      min-width: 880px;
    }}
    #region-chart svg, #bubble-map svg {{
      min-width: 0;
    }}
    .bar {{
      transition: width 180ms ease;
    }}
    .bubble {{
      opacity: 0.72;
      transition: opacity 150ms ease, r 150ms ease;
    }}
    .bubble:hover {{
      opacity: 1;
      stroke-width: 2.5;
    }}
    .empty-state {{
      padding: 32px;
      border: 1px dashed var(--grid);
      border-radius: 14px;
      color: var(--muted);
      text-align: center;
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
    @media (max-width: 860px) {{
      .chart-grid {{
        grid-template-columns: 1fr;
      }}
      svg {{
        min-width: 720px;
      }}
      #region-chart svg {{
        min-width: 0;
      }}
    }}
  </style>
</head>
<body>
  <main>
    <section class="hero">
      <h1>World Country Population Totals</h1>
      <p>Latest available World Bank population value for each country. The bar chart shows the top {top_n}; the table includes all countries in the dataset.</p>
      <div class="stats">
        <div class="stat"><strong id="country-count">{len(populations)}</strong><span>countries</span></div>
        <div class="stat"><strong id="combined-population">{format_population(total_population)}</strong><span>combined population</span></div>
        <div class="stat"><strong id="data-years">{years[0]}-{years[-1]}</strong><span>latest available years</span></div>
      </div>
      <p class="source">Source: World Bank indicator SP.POP.TOTL. Data snapshot: {html.escape(generated_at)}.</p>
      <div class="controls" aria-label="Interactive population controls">
        <label for="country-search">
          Search countries
          <input id="country-search" type="search" placeholder="India, Brazil, Nigeria..." autocomplete="off">
        </label>
        <label for="region-filter">
          Region
          <select id="region-filter">
            <option value="">All regions</option>
{region_options}
          </select>
        </label>
        <label for="top-count">
          Show top
          <select id="top-count">
{top_count_options}
          </select>
        </label>
        <label for="sort-mode">
          Sort by
          <select id="sort-mode">
            <option value="population-desc">Population high to low</option>
            <option value="population-asc">Population low to high</option>
            <option value="country-asc">Country A-Z</option>
          </select>
        </label>
      </div>
    </section>

    <div class="chart-grid">
      <section class="card">
        <h2 id="bar-chart-title">Top {top_n} countries by population</h2>
        <p class="chart-note">Use the controls above to filter, search, resize the ranking, and change sort order.</p>
        <div id="bar-chart">{svg}</div>
      </section>

      <section class="card">
        <h2>Population by region</h2>
        <p class="chart-note">Regional shares update with the same filters as the country list.</p>
        <div id="region-chart"></div>
      </section>

      <section class="card full">
        <h2>World population bubble map</h2>
        <p class="chart-note">Bubble size represents population. Hover a bubble to see the country and value.</p>
        <div id="bubble-map" class="map-frame"></div>
      </section>
    </div>

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
        <tbody id="country-table-body">
{rows}
        </tbody>
      </table>
    </section>
    <script id="population-data" type="application/json">{data_json}</script>
    <script>
{_interactive_script()}
    </script>
  </main>
</body>
</html>
"""


def _interactive_script() -> str:
    return r"""(() => {
  const populationData = JSON.parse(document.getElementById("population-data").textContent);
  const colors = ["#2563eb", "#0f766e", "#f97316", "#7c3aed", "#dc2626", "#0891b2", "#65a30d", "#c2410c"];
  const collator = new Intl.Collator("en");
  const elements = {
    search: document.getElementById("country-search"),
    region: document.getElementById("region-filter"),
    topCount: document.getElementById("top-count"),
    sortMode: document.getElementById("sort-mode"),
    countryCount: document.getElementById("country-count"),
    combinedPopulation: document.getElementById("combined-population"),
    dataYears: document.getElementById("data-years"),
    barTitle: document.getElementById("bar-chart-title"),
    barChart: document.getElementById("bar-chart"),
    regionChart: document.getElementById("region-chart"),
    bubbleMap: document.getElementById("bubble-map"),
    tableBody: document.getElementById("country-table-body"),
  };

  function formatPopulation(value) {
    if (value >= 1000000000) return `${(value / 1000000000).toFixed(2)}B`;
    if (value >= 1000000) return `${(value / 1000000).toFixed(1)}M`;
    if (value >= 1000) return `${(value / 1000).toFixed(1)}K`;
    return String(value);
  }

  function escapeHtml(value) {
    return String(value)
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#39;");
  }

  function filteredData() {
    const searchTerm = elements.search.value.trim().toLowerCase();
    const region = elements.region.value;
    const sortMode = elements.sortMode.value;
    const data = populationData.filter((item) => {
      const matchesSearch = !searchTerm || item.country.toLowerCase().includes(searchTerm) || item.iso3.toLowerCase().includes(searchTerm);
      const matchesRegion = !region || item.region === region;
      return matchesSearch && matchesRegion;
    });

    data.sort((a, b) => {
      if (sortMode === "population-asc") return a.population - b.population;
      if (sortMode === "country-asc") return collator.compare(a.country, b.country);
      return b.population - a.population;
    });

    return data;
  }

  function selectedSortLabel() {
    const option = elements.sortMode.options[elements.sortMode.selectedIndex];
    return option ? option.textContent.toLowerCase() : "selected sort";
  }

  function updateSummary(data) {
    const total = data.reduce((sum, item) => sum + item.population, 0);
    const years = data.map((item) => item.year);
    elements.countryCount.textContent = data.length.toLocaleString();
    elements.combinedPopulation.textContent = formatPopulation(total);
    elements.dataYears.textContent = years.length ? `${Math.min(...years)}-${Math.max(...years)}` : "-";
  }

  function renderBarChart(data) {
    const topCount = Number(elements.topCount.value);
    const visible = data.slice(0, topCount);
    const isPopulationDescending = elements.sortMode.value === "population-desc";
    elements.barTitle.textContent = isPopulationDescending
      ? `${visible.length ? "Top " + visible.length : "No"} countries by population`
      : `${visible.length || "No"} countries by ${selectedSortLabel()}`;
    if (!visible.length) {
      elements.barChart.innerHTML = '<div class="empty-state">No countries match the current filters.</div>';
      return;
    }

    const width = 1120;
    const labelWidth = 280;
    const barX = 320;
    const barMaxWidth = 650;
    const valueX = barX + barMaxWidth + 20;
    const rowHeight = 32;
    const height = 42 + visible.length * rowHeight;
    const maxPopulation = Math.max(...visible.map((item) => item.population));
    const rows = visible.map((item, index) => {
      const y = 34 + index * rowHeight;
      const barWidth = Math.max(3, Math.round((item.population / maxPopulation) * barMaxWidth));
      const fill = colors[index % colors.length];
      return `
        <text x="24" y="${y + 17}" fill="#94a3b8" font-size="12">${index + 1}</text>
        <text x="${labelWidth}" y="${y + 17}" text-anchor="end" fill="#172033" font-size="13">${escapeHtml(item.country)}</text>
        <rect class="bar" x="${barX}" y="${y + 4}" width="${barWidth}" height="19" rx="6" fill="${fill}"></rect>
        <text x="${valueX}" y="${y + 18}" fill="#172033" font-size="13">${formatPopulation(item.population)}</text>
      `;
    }).join("");

    elements.barChart.innerHTML = `
      <svg viewBox="0 0 ${width} ${height}" role="img" aria-label="Interactive country population ranking">
        <rect width="100%" height="100%" fill="#ffffff"></rect>
        <text x="${labelWidth}" y="18" text-anchor="end" fill="#64748b" font-size="12">Country</text>
        <text x="${barX}" y="18" fill="#64748b" font-size="12">Population</text>
        ${rows}
      </svg>
    `;
  }

  function renderRegionChart(data) {
    if (!data.length) {
      elements.regionChart.innerHTML = '<div class="empty-state">No regional data to show.</div>';
      return;
    }

    const totals = new Map();
    for (const item of data) totals.set(item.region, (totals.get(item.region) || 0) + item.population);
    const totalPopulation = [...totals.values()].reduce((sum, value) => sum + value, 0);
    const regions = [...totals.entries()].sort((a, b) => b[1] - a[1]).slice(0, colors.length);
    let offset = 25;
    const circles = regions.map(([region, value], index) => {
      const share = (value / totalPopulation) * 100;
      const circle = `
        <circle cx="120" cy="120" r="${78 - index * 7}" fill="none" stroke="${colors[index]}" stroke-width="8"
          stroke-dasharray="${share.toFixed(2)} ${Math.max(0, 100 - share).toFixed(2)}"
          stroke-dashoffset="${offset.toFixed(2)}" pathLength="100" transform="rotate(-90 120 120)">
          <title>${escapeHtml(region)}: ${formatPopulation(value)} (${share.toFixed(1)}%)</title>
        </circle>`;
      offset -= share;
      return circle;
    }).join("");

    const legend = regions.map(([region, value], index) => {
      const share = (value / totalPopulation) * 100;
      return `
        <div class="legend-row">
          <span class="legend-swatch" style="background:${colors[index]}"></span>
          <span>${escapeHtml(region)}</span>
          <strong>${share.toFixed(1)}%</strong>
        </div>`;
    }).join("");

    elements.regionChart.innerHTML = `
      <svg viewBox="0 0 240 240" role="img" aria-label="Population share by region">
        <circle cx="120" cy="120" r="88" fill="#f8fafc"></circle>
        ${circles}
        <text x="120" y="114" text-anchor="middle" fill="#172033" font-size="24" font-weight="700">${regions.length}</text>
        <text x="120" y="137" text-anchor="middle" fill="#64748b" font-size="12">regions</text>
      </svg>
      <div class="legend">${legend}</div>
    `;
  }

  function renderBubbleMap(data) {
    const points = data.filter((item) => Number.isFinite(item.latitude) && Number.isFinite(item.longitude));
    if (!points.length) {
      elements.bubbleMap.innerHTML = '<div class="empty-state">No coordinates are available for the current filters.</div>';
      return;
    }

    const width = 1080;
    const height = 520;
    const maxPopulation = Math.max(...points.map((item) => item.population));
    const grid = [-120, -60, 0, 60, 120].map((lon) => {
      const x = ((lon + 180) / 360) * width;
      return `<line x1="${x}" y1="28" x2="${x}" y2="${height - 28}" stroke="#e2e8f0"></line>`;
    }).join("") + [-60, -30, 0, 30, 60].map((lat) => {
      const y = ((90 - lat) / 180) * height;
      return `<line x1="32" y1="${y}" x2="${width - 32}" y2="${y}" stroke="#e2e8f0"></line>`;
    }).join("");

    const bubbles = points.map((item) => {
      const x = ((item.longitude + 180) / 360) * width;
      const y = ((90 - item.latitude) / 180) * height;
      const radius = 4 + Math.sqrt(item.population / maxPopulation) * 30;
      return `
        <circle class="bubble" cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="${radius.toFixed(1)}" fill="#2563eb" stroke="#1e40af">
          <title>${escapeHtml(item.country)}: ${formatPopulation(item.population)}</title>
        </circle>`;
    }).join("");

    elements.bubbleMap.innerHTML = `
      <svg viewBox="0 0 ${width} ${height}" role="img" aria-label="World population bubble map">
        <rect x="0" y="0" width="${width}" height="${height}" rx="24" fill="#f8fafc"></rect>
        ${grid}
        <text x="36" y="32" fill="#64748b" font-size="12">Approximate equirectangular placement from World Bank coordinates</text>
        ${bubbles}
      </svg>
    `;
  }

  function renderTable(data) {
    elements.tableBody.innerHTML = data.map((item, index) => `
      <tr>
        <td>${index + 1}</td>
        <td>${escapeHtml(item.country)}</td>
        <td>${escapeHtml(item.iso3)}</td>
        <td>${escapeHtml(item.region)}</td>
        <td>${item.year}</td>
        <td>${item.population.toLocaleString()}</td>
      </tr>
    `).join("");
  }

  function renderDashboard() {
    const data = filteredData();
    updateSummary(data);
    renderBarChart(data);
    renderRegionChart(data);
    renderBubbleMap(data);
    renderTable(data);
  }

  function initDashboard() {
    for (const control of [elements.search, elements.region, elements.topCount, elements.sortMode]) {
      control.addEventListener("input", renderDashboard);
      control.addEventListener("change", renderDashboard);
    }
    renderDashboard();
  }

  document.addEventListener("DOMContentLoaded", initDashboard);
})();"""


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


def _parse_float(value: Any) -> float | None:
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def _top_count_options(top_n: int) -> str:
    values = sorted({10, 20, 30, 50, top_n})
    return "\n".join(
        f'            <option value="{value}"{" selected" if value == top_n else ""}>Top {value}</option>'
        for value in values
    )


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
