;; expect: deny
;; Denylist globs apply at any depth.
(string-append "k=" (fs-read "sub/server.key"))
