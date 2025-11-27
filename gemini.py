import socket
import ssl

def gemini_request(url):
    """
    Send a Gemini protocol request and return the response header + body.
    Example URL: gemini://gemini.circumlunar.space/
    """

    # Parse URL manually
    assert url.startswith("gemini://")
    tmp = url[len("gemini://"):]
    host, _, path = tmp.partition("/")
    path = "/" + path

    # Gemini port
    port = 1965

    # TLS context (Gemini requires TLS)
    ctx = ssl.create_default_context()

    # IMPORTANT: Gemini uses TOFU (trust-on-first-use)
    # To allow connecting without certificate verification, uncomment:
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE

    with socket.create_connection((host, port)) as sock:
        with ctx.wrap_socket(sock, server_hostname=host) as ssock:
            # Send request: "<url>\r\n"
            ssock.sendall((url + "\r\n").encode("utf-8"))

            # Receive response
            data = ssock.recv(65535)
            return data.decode("utf-8")

if __name__ == "__main__":
    url = "gemini://gemlog.blue/users/suma/"
    print(gemini_request(url))

