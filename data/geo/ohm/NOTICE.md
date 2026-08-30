# OpenHistoricalMap London boundary slice

| Field | Value |
|---|---|
| Source | [OpenHistoricalMap](https://www.openhistoricalmap.org/) |
| API | [OHM Overpass](https://overpass-api.openhistoricalmap.org/api/) |
| Retrieved at | 2026-08-30T06:51:56Z |
| Raw format | Overpass OSM JSON with relation, way, and node metadata |
| Processed format | Validated `BorderLayer` snapshots at 1900 and 1965 |
| Default licence | CC0-1.0 when a feature has no `license=*` tag |
| Attribution | Map data courtesy of the OpenHistoricalMap project, in the public domain unless otherwise noted. |

The exact query, relation versions, target years, payload name, and SHA-256 are
in `manifest.json`. The payload is committed so tests and bakes never depend on
the live API. Refreshing it is an explicit source update: repeat the manifest
query against the endpoint, replace the payload, then review and update every
pin in the manifest in the same commit.

| Relation | Pinned version | Source dates | Explicit licence |
|---|---:|---|---|
| [Metropolitan Borough of Chelsea](https://www.openhistoricalmap.org/relation/2691852) | 7 | 1899 to 1964 | none |
| [Metropolitan Borough of Holborn](https://www.openhistoricalmap.org/relation/2693964) | 9 | 1900 to 1964-03-31 | CC0-1.0 |
| [Metropolitan Borough of Paddington](https://www.openhistoricalmap.org/relation/2693965) | 5 | 1900 to 1964 | CC0-1.0 |
| [London Borough of Westminster](https://www.openhistoricalmap.org/relation/2693967) | 9 | from 1965-04-01 | none |

OHM data is CC0/public domain unless a feature carries its own `license=*` tag.
The importer records explicit supported tags and excludes resolved unsupported
licences. See [OHM copyright](https://www.openhistoricalmap.org/copyright) and
the [licence tag contract](https://wiki.openstreetmap.org/wiki/OpenHistoricalMap/Tags/Key/license).
