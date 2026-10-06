import { geoEqualEarth, geoPath } from "d3-geo";
import type { Feature, FeatureCollection, Geometry } from "geojson";
import { feature } from "topojson-client";
import type { GeometryCollection, Topology } from "topojson-specification";
import world from "world-atlas/countries-110m.json";

/**
 * The world at 1:110m (Natural Earth via world-atlas, public domain), as SVG
 * paths keyed by ISO alpha-2 code. Loaded on demand: d3-geo, topojson and
 * the map are a separate chunk the Analytics page asks for when it draws.
 */

// ISO 3166-1 numeric → alpha-2, for the countries in the map: "242FJ" is Fiji.
const CODES =
  "242FJ834TZ732EH124CA840US398KZ860UZ598PG360ID032AR152CL180CD706SO404KE729SD148TD332HT214DO643RU044BS238FK578NO304GL260TF626TL710ZA426LS484MX858UY076BR068BO604PE170CO591PA188CR558NI340HN222SV320GT084BZ862VE328GY740SR250FR218EC630PR388JM192CU716ZW072BW516NA686SN466ML478MR204BJ562NE566NG120CM768TG288GH384CI324GN624GW430LR694SL854BF140CF178CG266GA226GQ894ZM454MW508MZ748SZ024AO108BI376IL422LB450MG275PS270GM788TN012DZ400JO784AE634QA414KW368IQ512OM548VU116KH764TH418LA104MM704VN408KP410KR496MN356IN050BD064BT524NP586PK004AF762TJ417KG795TM364IR760SY051AM752SE112BY804UA616PL040AT348HU498MD642RO440LT428LV233EE276DE100BG300GR792TR008AL191HR756CH442LU056BE528NL620PT724ES372IE540NC090SB554NZ036AU144LK156CN158TW380IT208DK826GB352IS031AZ268GE608PH458MY096BN705SI246FI703SK203CZ232ER392JP600PY887YE682SA010AQ196CY504MA818EG434LY231ET262DJ800UG646RW070BA807MK688RS499ME780TT728SS";
const alpha2 = new Map<string, string>();
for (let i = 0; i < CODES.length; i += 5) alpha2.set(CODES.slice(i, i + 3), CODES.slice(i + 3, i + 5));

export type Shape = { code: string; d: string };

const topo = world as unknown as Topology<{ countries: GeometryCollection<{ name: string }> }>;
const countries = (feature(topo, topo.objects.countries) as FeatureCollection<Geometry, { name: string }>).features.filter((f) => f.id !== "010"); // no Antarctica

/** Paths for a map `width` wide; its height is width × 0.47. */
export function worldShapes(width: number): Shape[] {
  const height = width * 0.47;
  const projection = geoEqualEarth().fitExtent(
    [
      [0, 2],
      [width, height - 2],
    ],
    { type: "FeatureCollection", features: countries } as FeatureCollection,
  );
  const path = geoPath(projection);
  const out: Shape[] = [];
  for (const f of countries as Array<Feature<Geometry, { name: string }>>) {
    const code = alpha2.get(String(f.id)) ?? (f.properties?.name === "Kosovo" ? "XK" : "");
    const d = path(f);
    if (d) out.push({ code, d });
  }
  return out;
}
