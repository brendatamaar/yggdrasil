package main

import (
	"fmt"
	"strings"
)

// The scripts below are the whole history of each repo. Changing any text,
// author or step order changes commit hashes, so tests and screenshots that
// pin them must be updated too.

const javaPkg = "src/main/java/com/example/acme/shop/"
const javaTestPkg = "src/test/java/com/example/acme/shop/"

// buildShopAPI: Maven backend. Special cases: feature/cart-totals is ahead
// of its upstream, mvnw.cmd has CRLF line endings, v0.1.0 tags.
func buildShopAPI(b *builder) {
	b.commit("main", ada, "chore: initial Maven project",
		f("pom.xml", pom("0.1.0-SNAPSHOT", false)),
		f("mvnw", mvnw),
		f("mvnw.cmd", strings.ReplaceAll(mvnwCmd, "\n", "\r\n")),
		f(".gitignore", "target/\n.idea/\n*.iml\n"),
		f("README.md", "# shop-api\n\nBackend for acme-shop.\n"),
		f(javaPkg+"ShopApiApplication.java", javaClass("", "@SpringBootApplication\npublic class ShopApiApplication {\n    public static void main(String[] args) {\n        SpringApplication.run(ShopApiApplication.class, args);\n    }\n}\n",
			"org.springframework.boot.SpringApplication", "org.springframework.boot.autoconfigure.SpringBootApplication")),
		f("src/main/resources/application.yml", appYML(false, false)))
	b.commit("main", bram, "feat(catalog): add product entity",
		f(javaPkg+"catalog/Product.java", javaClass("catalog", "public record Product(long id, String name, BigDecimal price) {}\n", "java.math.BigDecimal")))
	b.commit("main", bram, "feat(catalog): add product repository",
		f(javaPkg+"catalog/ProductRepository.java", javaClass("catalog", "public interface ProductRepository {\n    List<Product> findAll();\n\n    Optional<Product> findById(long id);\n}\n", "java.util.List", "java.util.Optional")))
	b.commit("main", chen, "feat(catalog): expose GET /products",
		f(javaPkg+"catalog/ProductController.java", productController(false)))
	b.commit("main", ada, "test(catalog): cover product listing",
		f(javaTestPkg+"catalog/ProductControllerTest.java", javaClass("catalog", "class ProductControllerTest {\n    @Test\n    void listsProducts() {\n        // TODO: use a fake repository\n    }\n}\n", "org.junit.jupiter.api.Test")))
	b.commit("main", dana, "build: add spring-boot-starter-validation",
		f("pom.xml", pom("0.1.0-SNAPSHOT", true)))
	b.commit("main", chen, "feat(cart): add cart model",
		f(javaPkg+"cart/Cart.java", javaClass("cart", "public record Cart(String id, List<CartItem> items) {}\n", "java.util.List")),
		f(javaPkg+"cart/CartItem.java", javaClass("cart", "public record CartItem(long productId, int quantity, BigDecimal unitPrice) {}\n", "java.math.BigDecimal")))
	b.commit("main", chen, "feat(cart): add cart endpoints",
		f(javaPkg+"cart/CartController.java", javaClass("cart", "@RestController\n@RequestMapping(\"/carts\")\npublic class CartController {\n    @GetMapping(\"/{id}\")\n    public Cart get(@PathVariable String id) {\n        return new Cart(id, List.of());\n    }\n}\n",
			"java.util.List", "org.springframework.web.bind.annotation.*")))
	b.commit("main", ada, "fix(catalog): return 404 for unknown product",
		f(javaPkg+"catalog/ProductController.java", productController(true)))
	b.commit("main", bram, "docs: describe local setup in README",
		f("README.md", "# shop-api\n\nBackend for acme-shop.\n\n## Run locally\n\n    ./mvnw spring-boot:run\n\nOn Windows use `mvnw.cmd spring-boot:run`. The API listens on port 8081.\n"))
	b.commit("main", dana, "feat(orders): add order entity and status",
		f(javaPkg+"orders/Order.java", javaClass("orders", "public record Order(String id, String cartId, OrderStatus status, BigDecimal total) {}\n", "java.math.BigDecimal")),
		f(javaPkg+"orders/OrderStatus.java", javaClass("orders", "public enum OrderStatus {\n    PENDING,\n    PAID,\n    SHIPPED,\n    CANCELLED\n}\n")))
	b.commit("main", dana, "feat(orders): place order from cart",
		f(javaPkg+"orders/OrderService.java", orderService(false, false)))
	b.commit("main", bram, "chore(config): read database URL from env",
		f("src/main/resources/application.yml", appYML(true, false)))
	b.commit("main", chen, "test(orders): cover order placement",
		f(javaTestPkg+"orders/OrderServiceTest.java", javaClass("orders", "class OrderServiceTest {\n    @Test\n    void placesPendingOrder() {\n        var order = new OrderService().place(\"cart-1\", BigDecimal.TEN);\n        assertEquals(OrderStatus.PENDING, order.status());\n    }\n}\n",
			"static org.junit.jupiter.api.Assertions.assertEquals", "java.math.BigDecimal", "org.junit.jupiter.api.Test")))
	b.tag("v0.1.0-rc.1", "main", author{}, "")
	b.commit("main", ada, "chore(release): 0.1.0",
		f("pom.xml", pom("0.1.0", true)))
	b.tag("v0.1.0", "main", ada, "shop-api 0.1.0\n\nFirst release: catalog, carts and orders.")
	b.commit("main", ada, "chore: start 0.2.0 development",
		f("pom.xml", pom("0.2.0-SNAPSHOT", true)))
	b.commit("main", bram, "refactor(cart): extract price calculation",
		f(javaPkg+"cart/PriceCalculator.java", priceCalculator(0)))
	b.commit("main", dana, "feat(api): add health endpoint",
		f("src/main/resources/application.yml", appYML(true, true)))
	b.commit("main", ada, "fix(orders): reject empty carts",
		f(javaPkg+"orders/OrderService.java", orderService(true, false)))
	b.push("main")
	b.pushTag("v0.1.0")

	b.branch("feature/cart-totals", "main")
	b.commit("feature/cart-totals", chen, "feat(cart): compute cart subtotal",
		f(javaPkg+"cart/PriceCalculator.java", priceCalculator(1)))
	b.push("feature/cart-totals")
	b.commit("feature/cart-totals", chen, "feat(cart): apply percentage discounts",
		f(javaPkg+"cart/PriceCalculator.java", priceCalculator(2)))
	b.commit("feature/cart-totals", ada, "test(cart): cover discount rounding",
		f(javaTestPkg+"cart/PriceCalculatorTest.java", javaClass("cart", "class PriceCalculatorTest {\n    @Test\n    void roundsDiscountToCents() {\n        var total = PriceCalculator.discount(new BigDecimal(\"9.99\"), 15);\n        assertEquals(new BigDecimal(\"8.49\"), total);\n    }\n}\n",
			"static org.junit.jupiter.api.Assertions.assertEquals", "java.math.BigDecimal", "org.junit.jupiter.api.Test")))

	b.branch("fix/order-rounding", "main")
	b.commit("fix/order-rounding", dana, "fix(orders): round totals half-up to cents",
		f(javaPkg+"orders/OrderService.java", orderService(true, true)))

	b.checkout("main")
	b.note("mvnw.cmd is committed with CRLF line endings")
}

// buildShopWeb: pnpm storefront. Special cases: main is behind origin/main,
// the working tree is dirty.
func buildShopWeb(b *builder) {
	b.commit("main", bram, "chore: scaffold Vite + TypeScript app",
		f("package.json", webPackage("0.1.0", false, false)),
		f("pnpm-lock.yaml", webLock(false)),
		f("tsconfig.json", "{\n  \"compilerOptions\": {\n    \"target\": \"ES2022\",\n    \"module\": \"ESNext\",\n    \"moduleResolution\": \"bundler\",\n    \"strict\": true\n  },\n  \"include\": [\"src\"]\n}\n"),
		f("index.html", "<!doctype html>\n<html lang=\"en\">\n  <head>\n    <meta charset=\"UTF-8\" />\n    <title>acme-shop</title>\n  </head>\n  <body>\n    <div id=\"app\"></div>\n    <script type=\"module\" src=\"/src/main.ts\"></script>\n  </body>\n</html>\n"),
		f("src/main.ts", webMain),
		f(".gitignore", "node_modules/\ndist/\n"),
		f("README.md", "# shop-web\n\nStorefront for acme-shop.\n\n    pnpm install\n    pnpm dev\n"))
	b.commit("main", bram, "feat(ui): add header and layout",
		f("src/layout.ts", webLayout(false)))
	b.commit("main", dana, "feat(catalog): list products from the API",
		f("src/api.ts", webAPI(false)),
		f("src/products.ts", webProducts(false)))
	b.commit("main", ada, "feat(cart): add cart store",
		f("src/cart.ts", "export interface CartLine {\n  productId: number;\n  quantity: number;\n}\n\nconst lines: CartLine[] = [];\n\nexport function addToCart(productId: number): void {\n  const line = lines.find((l) => l.productId === productId);\n  if (line) line.quantity++;\n  else lines.push({ productId, quantity: 1 });\n}\n\nexport function cartCount(): number {\n  return lines.reduce((n, l) => n + l.quantity, 0);\n}\n"))
	b.commit("main", dana, "style: add prettier config",
		f(".prettierrc", "{\n  \"singleQuote\": true,\n  \"printWidth\": 100\n}\n"))
	b.commit("main", chen, "feat(cart): show cart badge in header",
		f("src/layout.ts", webLayout(true)))
	b.commit("main", bram, "fix(catalog): handle empty product list",
		f("src/products.ts", webProducts(true)))
	b.commit("main", ada, "test(cart): add cart store tests",
		f("src/cart.test.ts", "import { describe, expect, it } from 'vitest';\nimport { addToCart, cartCount } from './cart';\n\ndescribe('cart', () => {\n  it('counts quantities', () => {\n    addToCart(1);\n    addToCart(1);\n    expect(cartCount()).toBe(2);\n  });\n});\n"),
		f("package.json", webPackage("0.1.0", true, false)),
		f("pnpm-lock.yaml", webLock(true)))
	b.commit("main", dana, "build: pin pnpm via packageManager",
		f("package.json", webPackage("0.1.0", true, true)))
	b.commit("main", chen, "feat(auth): add login form",
		f("src/login.ts", webLogin(5000)))
	b.commit("main", bram, "docs: add screenshots section to README",
		f("README.md", "# shop-web\n\nStorefront for acme-shop.\n\n    pnpm install\n    pnpm dev\n\n## Screenshots\n\nSee docs/screenshots (coming soon).\n"))
	b.commit("main", chen, "feat(i18n): add German translations",
		f("src/i18n.ts", "export const messages = {\n  en: { cart: 'Cart', checkout: 'Checkout' },\n  de: { cart: 'Warenkorb', checkout: 'Zur Kasse' },\n} as const;\n"))
	b.commit("main", dana, "fix(ui): keep header sticky on mobile",
		f("src/style.css", "header {\n  position: sticky;\n  top: 0;\n}\n"))
	b.commit("main", bram, "refactor(api): share fetch helper",
		f("src/api.ts", webAPI(true)))
	b.commit("main", ada, "feat(catalog): add product search",
		f("src/search.ts", "import type { Product } from './api';\n\nexport function search(products: Product[], query: string): Product[] {\n  const q = query.trim().toLowerCase();\n  return products.filter((p) => p.name.toLowerCase().includes(q));\n}\n"))
	b.push("main")

	b.branch("feature/checkout-page", "main")
	b.commit("feature/checkout-page", ada, "feat(checkout): add checkout page skeleton",
		f("src/checkout.ts", webCheckout(false)))
	b.commit("feature/checkout-page", ada, "feat(checkout): validate shipping address",
		f("src/checkout.ts", webCheckout(true)))
	b.push("feature/checkout-page")

	b.branch("fix/login-timeout", "main")
	b.commit("fix/login-timeout", bram, "fix(auth): raise login request timeout to 15s",
		f("src/login.ts", webLogin(15000)))

	// Teammates pushed to main after the last pull.
	b.remoteCommit("main", chen, "fix(auth): retry login once after a timeout",
		f("src/login.ts", webLogin(5000)+"\nexport const LOGIN_RETRIES = 1;\n"))
	b.remoteCommit("main", dana, "chore(release): 0.2.0",
		f("package.json", webPackage("0.2.0", true, true)))

	b.checkout("main")
	b.writeFile("src/main.ts", webMain+"\nconsole.debug('cart ready');\n")
	b.writeFile("src/debug.ts", "export const DEBUG = true;\n")
	b.note("dirty: src/main.ts modified, src/debug.ts untracked")
}

// buildShopAdmin: admin dashboard. Special case: feature/user-roles has
// diverged from its upstream.
func buildShopAdmin(b *builder) {
	b.commit("main", dana, "chore: initial admin dashboard",
		f("package.json", adminPackage("1.0.0", false)),
		f("src/index.js", "import { renderOrders } from './orders.js';\n\nrenderOrders(document.getElementById('app'));\n"),
		f(".gitignore", "node_modules/\n"),
		f("README.md", "# shop-admin\n\nAdmin dashboard for acme-shop.\n\n    npm install\n    npm start\n"))
	b.commit("main", dana, "feat(orders): add orders table",
		f("src/orders.js", adminOrders(0)))
	b.commit("main", ada, "feat(products): add product editor",
		f("src/products.js", adminProducts(false)))
	b.commit("main", chen, "fix(orders): sort by newest first",
		f("src/orders.js", adminOrders(1)))
	b.commit("main", ada, "feat(auth): require admin role",
		f("src/auth.js", adminAuth(false)))
	b.commit("main", bram, "docs: document admin roles",
		f("README.md", "# shop-admin\n\nAdmin dashboard for acme-shop.\n\n    npm install\n    npm start\n\n## Roles\n\n- admin: everything\n- support: orders only\n"))
	b.commit("main", chen, "feat(reports): add daily revenue widget",
		f("src/reports.js", adminReports(false)))
	b.commit("main", dana, "test: add smoke test",
		f("test/smoke.test.js", "import { test } from 'node:test';\nimport assert from 'node:assert/strict';\nimport { sortOrders } from '../src/orders.js';\n\ntest('sorts orders', () => {\n  assert.deepEqual(sortOrders([]), []);\n});\n"),
		f("package.json", adminPackage("1.0.0", true)))
	b.commit("main", ada, "refactor(products): split form fields",
		f("src/products.js", adminProducts(true)),
		f("src/fields.js", "export function field(label, name) {\n  return '<label>' + label + ' <input name=\"' + name + '\"></label>';\n}\n"))
	b.commit("main", bram, "chore(deps): bump dev dependencies",
		f("package.json", adminPackage("1.1.0", true)))
	b.commit("main", chen, "feat(orders): filter by status",
		f("src/orders.js", adminOrders(2)))
	b.commit("main", dana, "fix(auth): redirect to login on 401",
		f("src/auth.js", adminAuth(true)))
	b.commit("main", ada, "feat(settings): add store settings page",
		f("src/settings.js", "export const defaults = {\n  currency: 'EUR',\n  lowStockThreshold: 5,\n};\n"))
	b.commit("main", bram, "chore: add editorconfig",
		f(".editorconfig", "root = true\n\n[*]\nindent_style = space\nindent_size = 2\nend_of_line = lf\n"))
	b.commit("main", chen, "fix(reports): use UTC for daily totals",
		f("src/reports.js", adminReports(true)))
	b.commit("main", dana, "docs: add changelog",
		f("CHANGELOG.md", "# Changelog\n\n## 1.1.0\n\n- Filter orders by status\n- Store settings page\n"))
	b.push("main")

	b.branch("feature/user-roles", "main")
	b.commit("feature/user-roles", ada, "feat(users): list admin users",
		f("src/users.js", adminUsers("")))
	b.push("feature/user-roles")
	b.commit("feature/user-roles", ada, "feat(users): edit user roles",
		f("src/users.js", adminUsers("\nexport function setRole(user, role) {\n  return { ...user, role };\n}\n")))
	// A teammate pushed to the same branch in the meantime.
	b.remoteCommit("feature/user-roles", bram, "feat(users): show role badge",
		f("src/users.js", adminUsers("\nexport function roleBadge(user) {\n  return '<span class=\"badge\">' + user.role + '</span>';\n}\n")))

	b.branch("feature/sales-report", "main")
	b.commit("feature/sales-report", chen, "feat(reports): export sales as CSV",
		f("src/export.js", "export function toCSV(rows) {\n  return rows.map((r) => [r.date, r.total].join(',')).join('\\n');\n}\n"))
	b.commit("feature/sales-report", chen, "feat(reports): add date range picker",
		f("src/range.js", "export function lastDays(n, now = new Date()) {\n  const from = new Date(now);\n  from.setUTCDate(from.getUTCDate() - n);\n  return { from, to: now };\n}\n"))
	b.push("feature/sales-report")

	b.checkout("main")
}

// shop-api content

func pom(version string, validation bool) string {
	deps := "        <dependency>\n            <groupId>org.springframework.boot</groupId>\n            <artifactId>spring-boot-starter-web</artifactId>\n        </dependency>\n"
	if validation {
		deps += "        <dependency>\n            <groupId>org.springframework.boot</groupId>\n            <artifactId>spring-boot-starter-validation</artifactId>\n        </dependency>\n"
	}
	deps += "        <dependency>\n            <groupId>org.springframework.boot</groupId>\n            <artifactId>spring-boot-starter-test</artifactId>\n            <scope>test</scope>\n        </dependency>\n"
	return `<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
    <modelVersion>4.0.0</modelVersion>
    <parent>
        <groupId>org.springframework.boot</groupId>
        <artifactId>spring-boot-starter-parent</artifactId>
        <version>3.5.0</version>
    </parent>
    <groupId>com.example.acme</groupId>
    <artifactId>shop-api</artifactId>
    <version>` + version + `</version>
    <properties>
        <java.version>21</java.version>
    </properties>
    <dependencies>
` + deps + `    </dependencies>
</project>
`
}

const mvnw = `#!/bin/sh
# Maven wrapper stub for the acme-shop demo. It does not download Maven.
exec mvn "$@"
`

const mvnwCmd = `@REM Maven wrapper stub for the acme-shop demo. It does not download Maven.
@echo off
mvn %*
`

func javaClass(sub, body string, imports ...string) string {
	pkg := "com.example.acme.shop"
	if sub != "" {
		pkg += "." + sub
	}
	s := "package " + pkg + ";\n\n"
	for _, i := range imports {
		s += "import " + i + ";\n"
	}
	if len(imports) > 0 {
		s += "\n"
	}
	return s + body
}

func appYML(dbFromEnv, health bool) string {
	url := "jdbc:h2:mem:shop"
	if dbFromEnv {
		url = "${SHOP_DB_URL:jdbc:h2:mem:shop}"
	}
	s := "server:\n  port: 8081\nspring:\n  application:\n    name: shop-api\n  datasource:\n    url: " + url + "\n"
	if health {
		s += "management:\n  endpoints:\n    web:\n      exposure:\n        include: health\n"
	}
	return s
}

func productController(notFound bool) string {
	get := "        return products.findById(id).orElse(null);\n"
	if notFound {
		get = "        return products.findById(id)\n            .orElseThrow(() -> new ResponseStatusException(HttpStatus.NOT_FOUND));\n"
	}
	return javaClass("catalog", `@RestController
@RequestMapping("/products")
public class ProductController {
    private final ProductRepository products;

    public ProductController(ProductRepository products) {
        this.products = products;
    }

    @GetMapping
    public List<Product> list() {
        return products.findAll();
    }

    @GetMapping("/{id}")
    public Product get(@PathVariable long id) {
`+get+`    }
}
`, "java.util.List", "org.springframework.http.HttpStatus", "org.springframework.web.bind.annotation.*", "org.springframework.web.server.ResponseStatusException")
}

func orderService(rejectEmpty, roundHalfUp bool) string {
	body := ""
	if rejectEmpty {
		body += "        if (total.signum() == 0) {\n            throw new IllegalArgumentException(\"cart is empty\");\n        }\n"
	}
	if roundHalfUp {
		body += "        total = total.setScale(2, RoundingMode.HALF_UP);\n"
	}
	return javaClass("orders", `public class OrderService {
    public Order place(String cartId, BigDecimal total) {
`+body+`        return new Order(UUID.randomUUID().toString(), cartId, OrderStatus.PENDING, total);
    }
}
`, "java.math.BigDecimal", "java.math.RoundingMode", "java.util.UUID")
}

func priceCalculator(stage int) string {
	body := "    private PriceCalculator() {}\n"
	if stage >= 1 {
		body += "\n    public static BigDecimal subtotal(List<CartItem> items) {\n        return items.stream()\n            .map(i -> i.unitPrice().multiply(BigDecimal.valueOf(i.quantity())))\n            .reduce(BigDecimal.ZERO, BigDecimal::add);\n    }\n"
	}
	if stage >= 2 {
		body += "\n    public static BigDecimal discount(BigDecimal amount, int percent) {\n        var factor = BigDecimal.valueOf(100 - percent).movePointLeft(2);\n        return amount.multiply(factor).setScale(2, RoundingMode.HALF_UP);\n    }\n"
	}
	return javaClass("cart", "public final class PriceCalculator {\n"+body+"}\n", "java.math.BigDecimal", "java.math.RoundingMode", "java.util.List")
}

// shop-web content

func webPackage(version string, vitest, pinPnpm bool) string {
	test := "echo \\\"no tests yet\\\""
	devDeps := "    \"typescript\": \"5.9.3\",\n    \"vite\": \"8.3.4\"\n"
	if vitest {
		test = "vitest run"
		devDeps = "    \"typescript\": \"5.9.3\",\n    \"vite\": \"8.3.4\",\n    \"vitest\": \"5.0.4\"\n"
	}
	pm := ""
	if pinPnpm {
		pm = "  \"packageManager\": \"pnpm@10.18.0\",\n"
	}
	return fmt.Sprintf(`{
  "name": "@acme-shop/shop-web",
  "version": "%s",
  "private": true,
  "type": "module",
%s  "scripts": {
    "dev": "vite",
    "build": "tsc && vite build",
    "test": "%s"
  },
  "devDependencies": {
%s  }
}
`, version, pm, test, devDeps)
}

func webLock(vitest bool) string {
	s := "lockfileVersion: '9.0'\n\n# Stub lockfile for the acme-shop demo; it lists top-level packages only.\n\nimporters:\n  .:\n    devDependencies:\n      typescript:\n        specifier: 5.9.3\n        version: 5.9.3\n      vite:\n        specifier: 8.3.4\n        version: 8.3.4\n"
	if vitest {
		s += "      vitest:\n        specifier: 5.0.4\n        version: 5.0.4\n"
	}
	return s
}

const webMain = `import './style.css';
import { renderLayout } from './layout';
import { renderProducts } from './products';

const app = document.querySelector<HTMLDivElement>('#app')!;
renderLayout(app);
void renderProducts(app);
`

func webLayout(badge bool) string {
	header := "'<header>acme-shop</header><main></main>'"
	imp := ""
	if badge {
		header = "'<header>acme-shop <span class=\"badge\">' + cartCount() + '</span></header><main></main>'"
		imp = "import { cartCount } from './cart';\n\n"
	}
	return imp + "export function renderLayout(root: HTMLElement): void {\n  root.innerHTML = " + header + ";\n}\n"
}

func webAPI(shared bool) string {
	if !shared {
		return "export interface Product {\n  id: number;\n  name: string;\n  price: number;\n}\n\nexport async function listProducts(): Promise<Product[]> {\n  const res = await fetch('/api/products');\n  return res.json();\n}\n"
	}
	return "export interface Product {\n  id: number;\n  name: string;\n  price: number;\n}\n\nasync function get<T>(path: string): Promise<T> {\n  const res = await fetch('/api' + path);\n  if (!res.ok) throw new Error(path + ': ' + res.status);\n  return res.json() as Promise<T>;\n}\n\nexport const listProducts = () => get<Product[]>('/products');\n"
}

func webProducts(handleEmpty bool) string {
	empty := ""
	if handleEmpty {
		empty = "  if (products.length === 0) {\n    root.querySelector('main')!.textContent = 'No products yet.';\n    return;\n  }\n"
	}
	return "import { listProducts } from './api';\n\nexport async function renderProducts(root: HTMLElement): Promise<void> {\n  const products = await listProducts();\n" + empty + "  root.querySelector('main')!.innerHTML = products.map((p) => '<li>' + p.name + '</li>').join('');\n}\n"
}

func webLogin(timeoutMs int) string {
	return fmt.Sprintf("export const LOGIN_TIMEOUT_MS = %d;\n\nexport async function login(email: string, password: string): Promise<boolean> {\n  const res = await fetch('/api/login', {\n    method: 'POST',\n    body: JSON.stringify({ email, password }),\n    signal: AbortSignal.timeout(LOGIN_TIMEOUT_MS),\n  });\n  return res.ok;\n}\n", timeoutMs)
}

func webCheckout(validate bool) string {
	s := "export interface Address {\n  name: string;\n  street: string;\n  city: string;\n  postcode: string;\n}\n\nexport function renderCheckout(root: HTMLElement): void {\n  root.innerHTML = '<h1>Checkout</h1><form id=\"address\"></form>';\n}\n"
	if validate {
		s += "\nexport function validateAddress(a: Address): string[] {\n  return (Object.keys(a) as (keyof Address)[]).filter((k) => a[k].trim() === '');\n}\n"
	}
	return s
}

// shop-admin content

func adminPackage(version string, withTest bool) string {
	test := "echo \\\"no tests yet\\\""
	if withTest {
		test = "node --test"
	}
	return fmt.Sprintf(`{
  "name": "@acme-shop/shop-admin",
  "version": "%s",
  "private": true,
  "type": "module",
  "scripts": {
    "start": "node src/index.js",
    "test": "%s"
  }
}
`, version, test)
}

func adminOrders(stage int) string {
	s := "export function renderOrders(root) {\n  root.innerHTML = '<table id=\"orders\"></table>';\n}\n"
	if stage >= 1 {
		s += "\nexport function sortOrders(orders) {\n  return [...orders].sort((a, b) => b.createdAt.localeCompare(a.createdAt));\n}\n"
	}
	if stage >= 2 {
		s += "\nexport function filterOrders(orders, status) {\n  return status ? orders.filter((o) => o.status === status) : orders;\n}\n"
	}
	return s
}

func adminProducts(split bool) string {
	if split {
		return "import { field } from './fields.js';\n\nexport function productForm() {\n  return '<form>' + field('Name', 'name') + field('Price', 'price') + '</form>';\n}\n"
	}
	return "export function productForm() {\n  return '<form><input name=\"name\"><input name=\"price\"></form>';\n}\n"
}

func adminAuth(redirect bool) string {
	s := "export function requireAdmin(user) {\n  if (user.role !== 'admin') throw new Error('admin role required');\n}\n"
	if redirect {
		s += "\nexport async function authFetch(url) {\n  const res = await fetch(url);\n  if (res.status === 401) window.location.assign('/login');\n  return res;\n}\n"
	}
	return s
}

func adminReports(utc bool) string {
	day := "d.getDate()"
	if utc {
		day = "d.getUTCDate()"
	}
	return "export function dailyRevenue(orders) {\n  const totals = {};\n  for (const o of orders) {\n    const d = new Date(o.createdAt);\n    const key = " + day + ";\n    totals[key] = (totals[key] ?? 0) + o.total;\n  }\n  return totals;\n}\n"
}

func adminUsers(extra string) string {
	return "export async function listUsers() {\n  const res = await fetch('/api/admin/users');\n  return res.json();\n}\n" + extra
}
