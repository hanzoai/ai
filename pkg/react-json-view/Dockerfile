FROM node:14.15.4-buster-slim
LABEL maintainer="woo <woo@lux.network>"
# install the node modules at container build time
RUN mkdir -p /react
ADD package.json /react/package.json
RUN cd /react && npm install --save-dev --silent

## Now add our project code
ADD . /react
WORKDIR /react

EXPOSE 2000
ENTRYPOINT ["/react/entrypoints/build.sh"]

