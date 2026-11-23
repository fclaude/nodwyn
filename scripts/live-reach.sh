#!/usr/bin/env bash
# A second opinion, with tools other than the live suite's, on whether this
# job reaches the endpoint (ADR 63): what NODWYN_LIVE_URL's name resolves to,
# the route the job takes there, proxy settings, and what curl gets. It never
# fails the job, and it prints the address and host name replaced as the suite
# replaces them. The live workflows run it before the suite.
host=$(printf '%s' "$NODWYN_LIVE_URL" | sed -E 's#^[a-z]+://([^/:]+).*#\1#')
addrs=$(getent ahosts "$host" | awk '{print $1}' | sort -u)
scrub() {
	out=$(cat)
	out=${out//"${NODWYN_LIVE_URL%/}"/[live url]}
	out=${out//"$host"/[live host]}
	for a in $addrs; do out=${out//"$a"/[live address]}; done
	printf '%s\n' "$out"
}
echo "the endpoint's name resolves to $(printf '%s\n' $addrs | grep -c .) address(es)"
for a in $addrs; do ip route get "$a" 2>&1 | scrub; done
env | grep -i -E '^(https?|no|all)_proxy=' | sed 's/=.*/ is set/' || echo "no proxy variable is set"
if command -v curl >/dev/null; then
	curl -sS -m 20 -o /dev/null -w 'curl: HTTP %{http_code}\n' "${NODWYN_LIVE_URL%/}/api/tags" 2>&1 | scrub || true
else
	echo "curl is not installed"
fi
exit 0
