;; expect: deny
;; L1 (P4), ADR 62: what is typed into a page in the task's browser is an
;; egress payload, since the page can send it on to its site. A known-shape
;; key in it is refused outright, before the confirm every action on a page
;; asks: no binding names the listed site (shop.example) for its class, and
;; the browser, like a contained MCP server, is judged by every site it can
;; reach. The browser never starts.
(browser-type "e1" "token sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv")
