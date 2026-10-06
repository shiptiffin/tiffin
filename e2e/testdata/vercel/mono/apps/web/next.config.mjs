// A static site: next build writes it to out/.
const config = {
  output: "export",
  trailingSlash: true,
  images: { unoptimized: true },
};

export default config;
