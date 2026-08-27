# Reconstructed paleo-coastlines — provenance and licence

The `<year>.geojson` slices this directory holds at build time are derived from
the GPlates Web Service and are **not committed**; `make fetch-geo`
regenerates them. This notice is committed because it records what the build
produces and what citing it requires.

## Upstream

| | |
|---|---|
| Service | GPlates Web Service, `https://gws.gplates.org/reconstruct/coastlines/` |
| Hosted by | EarthByte / AuScope, NCI Australia |
| Service software | [GPlates/gplates-web-service](https://github.com/GPlates/gplates-web-service), GPL-2.0 |
| Plate model | `MERDITH2021` |
| Model data licence | CC-BY 4.0 ([Zenodo `10.5281/zenodo.10346399`](https://doi.org/10.5281/zenodo.10346399)) |
| Request | `?time=<Ma>&model=MERDITH2021&wrap=true` |

MERDITH2021 is used rather than the service default (`ZAHIROVIC2022`) because
the default stops at 410 Ma and cannot answer for the Cambrian at all;
MERDITH2021 reaches 1000 Ma.

## Required citation (CC-BY 4.0)

> Andrew S. Merdith, Simon E. Williams, Alan S. Collins, Michael G. Tetley,
> Jacob A. Mulder, Morgan L. Blades, Alexander Young, Sheree E. Armistead, John
> Cannon, Sabin Zahirovic, R. Dietmar Müller, (2021). Extending full-plate
> tectonic models into deep time: Linking the Neoproterozoic and the
> Phanerozoic, Earth-Science Reviews, Volume 214, 2021, 103477, ISSN 0012-8252,
> https://doi.org/10.1016/j.earscirev.2020.103477

## Modifications

Produced by `baker fetch-paleo` (`internal/ingest/paleo.go`), which is the
corresponding source. It requests 36 reconstruction times from 540 Ma to 1 Ma,
then simplifies each with Douglas-Peucker (0.35°-0.9°, ~39-100 km), quantizes
coordinates to 4 decimal places, drops sub-tolerance specks, repairs pinched
and self-crossing rings, and rewinds rings to RFC 7946. No coastline was moved
or invented.

`wrap=true` is not optional: without it a landmass straddling the antimeridian
comes back running the long way round and paints a band across the whole map.

## What these shapes are, and are not

They are **present-day coastlines carried back to where the plate model says
those rocks were** — not the shorelines of the time. Sea level, basin
subsidence and erosion since then are not modelled, so a coast drawn at 250 Ma
shows where today's coastline sat, not where the water met the land. Continents
are recognisable and their arrangement is the science; the outlines are not.

Deep-time reconstructions carry real uncertainty that grows with age, and the
~40-100 km simplification here is well inside it. Every feature is marked
`representation: "estimated"`.
