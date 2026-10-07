import Link from "next/link";
import { Mark } from "./chrome";

export const metadata = { title: "Not found" };

export default function NotFound() {
  return (
    <section className="lost">
      <div className="wrap">
        <Mark />
        <h1 className="h2">Nothing at this address.</h1>
        <p className="section-sub">The page may have moved, or the link has a typo.</p>
        <div className="actions">
          <Link className="btn btn-quiet" href="/">
            Back to the home page
          </Link>
        </div>
      </div>
    </section>
  );
}
