export default {
  async fetch(request) {
    const destination = new URL(request.url);
    destination.protocol = "https:";
    destination.hostname = "www.testagram.site";
    destination.port = "";
    return Response.redirect(destination.toString(), 308);
  },
};
