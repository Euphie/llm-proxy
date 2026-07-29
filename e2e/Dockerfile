FROM mcr.microsoft.com/playwright:v1.62.0-noble

WORKDIR /tests

COPY package.json package-lock.json ./
RUN npm ci

COPY playwright.config.js admin.spec.js ./

CMD ["npx", "playwright", "test"]
