// Sets the theme before first paint (an external file: the CSP allows no inline scripts).
(function () {
  var t = null;
  try { t = localStorage.getItem("tiffin.theme"); } catch (e) {}
  if (t !== "light" && t !== "dark") t = matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  document.documentElement.dataset.theme = t;
})();
