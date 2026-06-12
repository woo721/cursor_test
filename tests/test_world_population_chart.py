import unittest

from scripts.generate_world_population_chart import (
    build_chart_html,
    format_population,
    load_country_metadata,
    latest_country_populations,
)


class WorldPopulationChartTests(unittest.TestCase):
    def test_load_country_metadata_filters_world_bank_aggregates(self):
        records = [
            {
                "id": "WLD",
                "name": "World",
                "region": {"id": "NA", "value": "Aggregates"},
            },
            {
                "id": "IND",
                "name": "India",
                "region": {"id": "SAS", "value": "South Asia"},
            },
        ]

        countries = load_country_metadata(records)

        self.assertEqual(countries, {"IND": {"name": "India", "region": "South Asia"}})

    def test_latest_country_populations_filters_aggregates_and_uses_latest_value(self):
        countries = {
            "IND": {"name": "India", "region": "South Asia"},
            "USA": {"name": "United States", "region": "North America"},
        }
        records = [
            {"countryiso3code": "IND", "date": "2023", "value": 1_428_600_000},
            {"countryiso3code": "IND", "date": "2024", "value": 1_441_700_000},
            {"countryiso3code": "USA", "date": "2024", "value": 341_800_000},
            {"countryiso3code": "USA", "date": "2025", "value": None},
            {"countryiso3code": "WLD", "date": "2024", "value": 8_161_900_000},
        ]

        populations = latest_country_populations(records, countries)

        self.assertEqual([item["iso3"] for item in populations], ["IND", "USA"])
        self.assertEqual(populations[0]["population"], 1_441_700_000)
        self.assertEqual(populations[0]["year"], 2024)
        self.assertEqual(populations[1]["population"], 341_800_000)
        self.assertEqual(populations[1]["year"], 2024)

    def test_format_population_uses_human_readable_units(self):
        self.assertEqual(format_population(1_441_700_000), "1.44B")
        self.assertEqual(format_population(341_800_000), "341.8M")
        self.assertEqual(format_population(950_000), "950.0K")

    def test_build_chart_html_contains_svg_chart_and_full_country_table(self):
        populations = [
            {
                "iso3": "IND",
                "country": "India",
                "region": "South Asia",
                "population": 1_441_700_000,
                "year": 2024,
            },
            {
                "iso3": "USA",
                "country": "United States",
                "region": "North America",
                "population": 341_800_000,
                "year": 2024,
            },
        ]

        html = build_chart_html(populations, generated_at="2026-06-12T03:42:00Z")

        self.assertIn("<svg", html)
        self.assertIn("Top 30 countries by population", html)
        self.assertIn("India", html)
        self.assertIn("United States", html)
        self.assertIn("2026-06-12T03:42:00Z", html)
        self.assertIn("<table", html)

    def test_build_chart_html_defaults_to_deterministic_data_snapshot_label(self):
        populations = [
            {
                "iso3": "IND",
                "country": "India",
                "region": "South Asia",
                "population": 1_441_700_000,
                "year": 2024,
            },
        ]

        html = build_chart_html(populations)

        self.assertIn("latest available data years 2024-2024", html)


if __name__ == "__main__":
    unittest.main()
